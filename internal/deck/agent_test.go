package deck

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/host"
	"github.com/Raindancer118/pocketsudo/internal/relay"
	"github.com/Raindancer118/pocketsudo/protocol"
)

// fakeSys records what the agent would run on the desktop.
type fakeSys struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
	lines chan string // what the clipboard watcher "sees"
}

func (f *fakeSys) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	for prefix, out := range f.out {
		if strings.HasPrefix(line, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeSys) Stream(ctx context.Context, onLine func(string), name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	lines := f.lines
	f.mu.Unlock()
	if lines == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	for {
		select {
		case l := <-lines:
			onLine(l)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *fakeSys) Start(ctx context.Context, name string, args ...string) error {
	_, err := f.Run(ctx, name, args...)
	return err
}

func (f *fakeSys) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type rig struct {
	t        *testing.T
	agent    *Agent
	sys      *fakeSys
	key      []byte
	phoneKey *ecdsa.PrivateKey
	phone    *host.Client
	channel  string
	after    int64
}

// newRig pairs a fake phone with a real relay and starts the agent loop.
func newRig(t *testing.T) *rig {
	t.Helper()
	r, err := relay.New(relay.Options{DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cfg := &Config{path: t.TempDir() + "/config.json"}
	phoneSign, _ := protocol.GenerateKey()
	phoneKX, _ := protocol.GenerateKX()
	phoneSignPub, _ := protocol.EncodePublicKey(&phoneSign.PublicKey)
	phoneKXPub, _ := protocol.EncodeKXPublic(phoneKX.PublicKey())
	var phoneToken, channel string
	err = Pair(context.Background(), cfg, srv.URL, "eclipse", host.PairUI{Out: io.Discard,
		ShowQR: func(uri string) {
			u := mustParse(t, uri)
			channel = u.channel
			c := host.NewClient(srv.URL, "")
			var reg protocol.RegisterPhoneResp
			c.Do(context.Background(), "POST", "/v1/channels/"+channel+"/phone", protocol.RegisterPhoneReq{
				PhonePub: phoneSignPub, PhoneKX: phoneKXPub, DeviceName: "S24",
				MAC: protocol.PairMACv2(u.secret, channel, phoneSignPub, phoneKXPub, "S24")}, &reg)
			phoneToken = reg.PhoneToken
		},
		Confirm: func(string, string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocol.DeckKey(phoneKX, cfg.HostKXPub, channel)
	if err != nil {
		t.Fatal(err)
	}
	sys := &fakeSys{lines: make(chan string, 4), out: map[string]string{
		"wpctl get-volume": "Volume: 0.62\n",
		"brightnessctl -m": "intel_backlight,backlight,176,35%,496\n",
		"pidof hyprlock":   "1234\n",
	}}
	a, err := NewAgent(cfg, sys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.batteryDir = t.TempDir()
	a.hostname = "eclipse"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.Run(ctx)
	return &rig{t: t, agent: a, sys: sys, key: key, phoneKey: phoneSign, phone: host.NewClient(srv.URL, phoneToken), channel: channel}
}

// send seals a command as the phone and waits for the agent's answer.
func (r *rig) send(typ string, body any, auth bool) protocol.DeckMessage {
	r.t.Helper()
	b, _ := json.Marshal(body)
	m := protocol.DeckMessage{ID: protocol.RandomID(16), Type: typ, TS: time.Now().Unix(), Body: b}
	if auth {
		m.Sig, _ = protocol.SignDeckAuth(r.phoneKey, m)
	}
	box, _ := protocol.SealMessage(r.key, m, time.Now())
	if _, err := r.phone.Do(context.Background(), "POST", "/v1/channels/"+r.channel+"/inbox/host", protocol.InboxPost{Box: box}, nil); err != nil {
		r.t.Fatal(err)
	}
	return r.await(m.ID)
}

func (r *rig) await(ref string) protocol.DeckMessage {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var in protocol.InboxResp
		r.phone.Do(context.Background(), "GET", "/v1/channels/"+r.channel+"/inbox/phone?wait=2&after="+itoa(r.after), nil, &in)
		for _, msg := range in.Messages {
			r.after = msg.Seq
			got, err := protocol.OpenMessage(r.key, msg.Box, time.Now(), nil)
			if err != nil {
				r.t.Fatalf("agent sent a box the phone cannot open: %v", err)
			}
			if got.Ref == ref {
				return got
			}
		}
	}
	r.t.Fatalf("no answer to %s", ref)
	return protocol.DeckMessage{}
}

func result(t *testing.T, m protocol.DeckMessage) Result {
	t.Helper()
	var res Result
	if err := json.Unmarshal(m.Body, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestStateReport(t *testing.T) {
	r := newRig(t)
	res := result(t, r.send("state", nil, false))
	if !res.OK {
		t.Fatalf("state failed: %+v", res)
	}
	var st State
	json.Unmarshal(res.Data, &st)
	if st.Hostname != "eclipse" || !st.Locked || st.Volume != 62 || st.Brightness != 35 {
		t.Fatalf("state %+v", st)
	}
}

func TestUnlockNeedsFingerprint(t *testing.T) {
	r := newRig(t)
	if res := result(t, r.send("unlock", nil, false)); res.OK || r.sys.ran("pkill -USR1 hyprlock") {
		t.Fatalf("unlocked without a signature: %+v", res)
	}
	if res := result(t, r.send("unlock", nil, true)); !res.OK || !r.sys.ran("pkill -USR1 hyprlock") {
		t.Fatalf("signed unlock failed: %+v", res)
	}
}

func TestEverydayActionsRunWithoutFingerprint(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		typ  string
		body any
		want string
	}{
		{"volume.set", map[string]int{"percent": 40}, "wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.40"},
		{"brightness.set", map[string]int{"percent": 70}, "brightnessctl set 70%"},
		{"media", map[string]string{"action": "next"}, "playerctl next"},
		{"lock", nil, "loginctl lock-session"},
		{"open.url", map[string]string{"url": "https://example.com"}, "xdg-open https://example.com"},
		{"clipboard.set", map[string]string{"text": "hallo"}, "wl-copy"},
	}
	for _, c := range cases {
		if res := result(t, r.send(c.typ, c.body, false)); !res.OK || !r.sys.ran(c.want) {
			t.Errorf("%s: ok=%v err=%q, calls=%v", c.typ, res.OK, res.Error, r.sys.calls)
		}
	}
}

func TestRejectsBadInput(t *testing.T) {
	r := newRig(t)
	if res := result(t, r.send("open.url", map[string]string{"url": "file:///etc/shadow"}, false)); res.OK {
		t.Fatal("opened a non-http url")
	}
	if res := result(t, r.send("volume.set", map[string]int{"percent": 400}, false)); res.OK {
		t.Fatal("accepted 400 % volume")
	}
	if res := result(t, r.send("no.such.action", nil, false)); res.OK {
		t.Fatal("unknown action reported ok")
	}
	if res := result(t, r.send("command", map[string]string{"cmd": "touch /tmp/x"}, false)); res.OK {
		t.Fatal("shell command without fingerprint")
	}
}

func TestChainsNeedFingerprintOnlyForShellSteps(t *testing.T) {
	r := newRig(t)
	safe := Chain{Steps: []Step{{Type: "workspace", Args: json.RawMessage(`{"id":3}`)}, {Type: "volume.set", Args: json.RawMessage(`{"percent":20}`)}}}
	if res := result(t, r.send("run", safe, false)); !res.OK || !r.sys.ran("wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.20") {
		t.Fatalf("safe chain: %+v", res)
	}
	risky := Chain{Steps: []Step{{Type: "command", Args: json.RawMessage(`{"cmd":"echo hi"}`)}}}
	if res := result(t, r.send("run", risky, false)); res.OK || r.sys.ran("sh -c echo hi") {
		t.Fatal("chain with a shell step ran without fingerprint")
	}
	if res := result(t, r.send("run", risky, true)); !res.OK || !r.sys.ran("sh -c echo hi") {
		t.Fatalf("signed chain: %+v", res)
	}
}

func TestForeignBoxesAreIgnored(t *testing.T) {
	r := newRig(t)
	wrong := make([]byte, 32)
	box, _ := protocol.SealMessage(wrong, protocol.DeckMessage{ID: protocol.RandomID(16), Type: "lock"}, time.Now())
	r.phone.Do(context.Background(), "POST", "/v1/channels/"+r.channel+"/inbox/host", protocol.InboxPost{Box: box}, nil)
	// a valid command afterwards still works and nothing was locked by the forged one
	if res := result(t, r.send("state", nil, false)); !res.OK {
		t.Fatal("agent stuck after a forged box")
	}
	if r.sys.ran("loginctl lock-session") {
		t.Fatal("forged box executed")
	}
}

type pairURI struct {
	channel string
	secret  []byte
}

func mustParse(t *testing.T, uri string) pairURI {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "pocketdeck" {
		t.Fatalf("bad pair uri %q", uri)
	}
	q := u.Query()
	s, err := base64.RawURLEncoding.DecodeString(q.Get("s"))
	if err != nil {
		t.Fatal(err)
	}
	return pairURI{channel: q.Get("c"), secret: s}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestKeyboardNeedsSession(t *testing.T) {
	r := newRig(t)
	if res := result(t, r.send("key", map[string]string{"combo": "ctrl+alt+t"}, false)); res.OK || r.sys.ran("wtype") {
		t.Fatalf("key without a session: %+v", res)
	}
	if res := result(t, r.send("session.open", nil, false)); res.OK {
		t.Fatal("session opened without fingerprint")
	}
	if res := result(t, r.send("session.open", nil, true)); !res.OK {
		t.Fatalf("signed session.open failed: %+v", res)
	}
	if res := result(t, r.send("key", map[string]string{"combo": "ctrl+alt+t"}, false)); !res.OK ||
		!r.sys.ran("wtype -M ctrl -M alt -k t -m alt -m ctrl") {
		t.Fatalf("combo: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("key", map[string]string{"combo": "super"}, false)); !res.OK || !r.sys.ran("wtype -k Super_L") {
		t.Fatalf("super: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("type", map[string]string{"text": "ls -la /tmp ü"}, false)); !res.OK || !r.sys.ran("wtype -- ls -la /tmp ü") {
		t.Fatalf("type: %+v", res)
	}
	if res := result(t, r.send("key", map[string]string{"combo": "ctrl+rm -rf"}, false)); res.OK {
		t.Fatal("accepted a garbage key name")
	}
	if res := result(t, r.send("session.close", nil, false)); !res.OK {
		t.Fatal("close failed")
	}
	if res := result(t, r.send("key", map[string]string{"combo": "a"}, false)); res.OK {
		t.Fatal("key after session.close")
	}
}

func TestScreenshotAndFileTransfer(t *testing.T) {
	r := newRig(t)
	r.sys.out["grim"] = "\xff\xd8JPEGDATA"
	r.sys.mu.Lock()
	r.sys.mu.Unlock()
	res := result(t, r.send("screenshot", nil, false))
	if res.OK {
		t.Fatal("screenshot without a remote session")
	}
	result(t, r.send("session.open", nil, true))
	res = result(t, r.send("screenshot", nil, false))
	if !res.OK {
		t.Fatalf("screenshot: %+v", res)
	}
	var shot struct{ Blob string }
	json.Unmarshal(res.Data, &shot)
	var raw []byte
	if _, err := r.phone.Raw(context.Background(), "GET", "/v1/channels/"+r.channel+"/blobs/"+shot.Blob, nil, &raw); err != nil {
		t.Fatal(err)
	}
	plain, err := protocol.OpenBytes(r.key, raw)
	if err != nil || string(plain) != "\xff\xd8JPEGDATA" {
		t.Fatalf("screenshot blob: %v %q", err, plain)
	}

	// phone -> notebook: sealed upload, then file.put
	dir := t.TempDir()
	r.agent.downloadDir = dir
	box, _ := protocol.SealBytes(r.key, []byte("hallo datei"))
	var up protocol.BlobResp
	if _, err := r.phone.Raw(context.Background(), "PUT", "/v1/channels/"+r.channel+"/blobs", box, &up); err != nil {
		t.Fatal(err)
	}
	res = result(t, r.send("file.put", map[string]string{"blob": up.ID, "name": "../../evil/notiz.txt"}, false))
	if !res.OK {
		t.Fatalf("file.put: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "notiz.txt"))
	if err != nil || string(got) != "hallo datei" {
		t.Fatalf("saved file: %v %q", err, got)
	}
	// same name again does not overwrite
	box2, _ := protocol.SealBytes(r.key, []byte("zweite"))
	r.phone.Raw(context.Background(), "PUT", "/v1/channels/"+r.channel+"/blobs", box2, &up)
	result(t, r.send("file.put", map[string]string{"blob": up.ID, "name": "notiz.txt"}, false))
	if b, _ := os.ReadFile(filepath.Join(dir, "notiz (1).txt")); string(b) != "zweite" {
		t.Fatal("second file overwrote the first or was not saved")
	}
}

func TestClipboardFollowsToPhone(t *testing.T) {
	r := newRig(t)
	r.sys.lines <- base64.StdEncoding.EncodeToString([]byte("was schon drin war"))
	r.sys.lines <- base64.StdEncoding.EncodeToString([]byte("kopiert am Notebook"))
	m := r.awaitType("clipboard")
	var body struct{ Text string }
	json.Unmarshal(m.Body, &body)
	if body.Text != "kopiert am Notebook" {
		t.Fatalf("clipboard event %q", body.Text)
	}
	// what the phone just sent must not bounce back
	result(t, r.send("clipboard.set", map[string]string{"text": "vom Handy"}, false))
	r.sys.lines <- base64.StdEncoding.EncodeToString([]byte("vom Handy"))
	r.sys.lines <- base64.StdEncoding.EncodeToString([]byte("danach"))
	m = r.awaitType("clipboard")
	json.Unmarshal(m.Body, &body)
	if body.Text != "danach" {
		t.Fatalf("echo not suppressed, got %q", body.Text)
	}
}

func (r *rig) awaitType(typ string) protocol.DeckMessage {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var in protocol.InboxResp
		r.phone.Do(context.Background(), "GET", "/v1/channels/"+r.channel+"/inbox/phone?wait=2&after="+itoa(r.after), nil, &in)
		for _, msg := range in.Messages {
			r.after = msg.Seq
			got, err := protocol.OpenMessage(r.key, msg.Box, time.Now(), nil)
			if err == nil && got.Type == typ {
				return got
			}
		}
	}
	r.t.Fatalf("no %s message", typ)
	return protocol.DeckMessage{}
}

func TestFileBrowser(t *testing.T) {
	r := newRig(t)
	home := t.TempDir()
	r.agent.home = home
	os.MkdirAll(filepath.Join(home, "Projekte"), 0o755)
	os.WriteFile(filepath.Join(home, "notiz.txt"), []byte("inhalt"), 0o644)
	os.WriteFile(filepath.Join(home, ".geheim"), []byte("x"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "passwd"), []byte("root"), 0o644)
	os.Symlink(outside, filepath.Join(home, "raus"))

	if res := result(t, r.send("fs.list", map[string]string{"path": ""}, false)); res.OK {
		t.Fatal("listing without a remote session")
	}
	result(t, r.send("session.open", nil, true))

	res := result(t, r.send("fs.list", map[string]string{"path": ""}, false))
	var ls struct {
		Path    string
		Entries []struct {
			Name string
			Dir  bool
			Size int64
		}
	}
	json.Unmarshal(res.Data, &ls)
	names := map[string]bool{}
	for _, e := range ls.Entries {
		names[e.Name] = e.Dir
	}
	if !res.OK || !names["Projekte"] || names["notiz.txt"] || len(names) < 2 {
		t.Fatalf("listing %+v %+v", res, ls)
	}
	if _, hidden := names[".geheim"]; hidden {
		t.Fatal("hidden files listed by default")
	}
	for _, bad := range []string{"..", "../..", "/etc", "raus", "raus/passwd"} {
		if res := result(t, r.send("fs.list", map[string]string{"path": bad}, false)); res.OK {
			t.Fatalf("escaped home via %q", bad)
		}
		if res := result(t, r.send("fs.get", map[string]string{"path": bad}, false)); res.OK {
			t.Fatalf("downloaded outside home via %q", bad)
		}
	}

	res = result(t, r.send("fs.get", map[string]string{"path": "notiz.txt"}, false))
	var got struct{ Blob, Name string }
	json.Unmarshal(res.Data, &got)
	var raw []byte
	r.phone.Raw(context.Background(), "GET", "/v1/channels/"+r.channel+"/blobs/"+got.Blob, nil, &raw)
	if plain, err := protocol.OpenBytes(r.key, raw); err != nil || string(plain) != "inhalt" || got.Name != "notiz.txt" {
		t.Fatalf("download: %v %q %+v", err, plain, got)
	}

	box, _ := protocol.SealBytes(r.key, []byte("hochgeladen"))
	var up protocol.BlobResp
	r.phone.Raw(context.Background(), "PUT", "/v1/channels/"+r.channel+"/blobs", box, &up)
	res = result(t, r.send("file.put", map[string]string{"blob": up.ID, "name": "neu.txt", "dir": "Projekte"}, false))
	if b, _ := os.ReadFile(filepath.Join(home, "Projekte", "neu.txt")); !res.OK || string(b) != "hochgeladen" {
		t.Fatalf("upload into folder: %+v", res)
	}
	r.phone.Raw(context.Background(), "PUT", "/v1/channels/"+r.channel+"/blobs", box, &up)
	if res := result(t, r.send("file.put", map[string]string{"blob": up.ID, "name": "x", "dir": "raus"}, false)); res.OK {
		t.Fatal("uploaded outside home")
	}
}

func TestGermanKeysAndPunctuation(t *testing.T) {
	r := newRig(t)
	result(t, r.send("session.open", nil, true))
	for combo, want := range map[string]string{
		"ssharp":           "wtype -k ssharp",
		"shift+udiaeresis": "wtype -M shift -k udiaeresis -m shift",
		"plus":             "wtype -k plus",
		"ctrl+minus":       "wtype -M ctrl -k minus -m ctrl",
		"altgr+q":          "wtype -M altgr -k q -m altgr",
		"capslock":         "wtype -k Caps_Lock",
		"less":             "wtype -k less",
	} {
		if res := result(t, r.send("key", map[string]string{"combo": combo}, false)); !res.OK || !r.sys.ran(want) {
			t.Errorf("%s: %+v (want %q)", combo, res, want)
		}
	}
}
