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
	sys := &fakeSys{out: map[string]string{
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
