package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Raindancer118/pocketsudo/protocol"
)

// pairDeck pairs a "deck" channel including key-exchange keys.
func (e *env) pairDeck() *env {
	e.t.Helper()
	hostPub, _ := protocol.EncodePublicKey(&e.host.PublicKey)
	hostKX, _ := protocol.GenerateKX()
	hostKXPub, _ := protocol.EncodeKXPublic(hostKX.PublicKey())
	var created protocol.CreateChannelResp
	if c := e.do("POST", "/v1/channels", "", protocol.CreateChannelReq{HostPub: hostPub, Hostname: "eclipse", App: protocol.AppDeck, HostKX: hostKXPub}, &created); c != 201 {
		e.t.Fatalf("create: %d", c)
	}
	e.ch, e.hostT = created.Channel, created.HostToken
	var info protocol.HostInfo
	e.do("GET", "/v1/channels/"+e.ch+"/host", "", nil, &info)
	if info.App != protocol.AppDeck || info.HostKX != hostKXPub {
		e.t.Fatalf("host info lost deck fields: %+v", info)
	}
	phonePub, _ := protocol.EncodePublicKey(&e.phone.PublicKey)
	phoneKX, _ := protocol.GenerateKX()
	phoneKXPub, _ := protocol.EncodeKXPublic(phoneKX.PublicKey())
	var reg protocol.RegisterPhoneResp
	e.do("POST", "/v1/channels/"+e.ch+"/phone", "", protocol.RegisterPhoneReq{PhonePub: phonePub, PhoneKX: phoneKXPub,
		DeviceName: "S24", FCMToken: "tokD", MAC: "m"}, &reg)
	e.phoneT = reg.PhoneToken
	var pairing protocol.PairingInfo
	e.do("GET", "/v1/channels/"+e.ch+"/pairing?wait=1", e.hostT, nil, &pairing)
	if pairing.PhoneKX != phoneKXPub {
		e.t.Fatal("phone kx not handed to the host")
	}
	e.do("POST", "/v1/channels/"+e.ch+"/confirm", e.hostT, protocol.ConfirmReq{Accept: true}, nil)
	return e
}

func TestInboxBothDirections(t *testing.T) {
	e := newEnv(t).pairDeck()
	base := "/v1/channels/" + e.ch + "/inbox/"
	// phone -> host
	var posted protocol.InboxPostResp
	if c := e.do("POST", base+"host", e.phoneT, protocol.InboxPost{Box: "AAAA"}, &posted); c != 201 || posted.Seq == 0 {
		t.Fatalf("phone post: %d %+v", c, posted)
	}
	if c := e.do("POST", base+"host", e.hostT, protocol.InboxPost{Box: "x"}, nil); c != 401 {
		t.Fatalf("host must not write into its own inbox: %d", c)
	}
	var got protocol.InboxResp
	e.do("GET", base+"host?after=0&wait=1", e.hostT, nil, &got)
	if len(got.Messages) != 1 || got.Messages[0].Box != "AAAA" {
		t.Fatalf("host inbox %+v", got)
	}
	if c := e.do("GET", base+"host", e.phoneT, nil, nil); c != 401 {
		t.Fatalf("phone must not read the host inbox: %d", c)
	}
	// after= hides what was already read
	e.do("GET", base+"host?after="+itoa(got.Messages[0].Seq)+"&wait=0", e.hostT, nil, &got)
	if len(got.Messages) != 0 {
		t.Fatal("after cursor ignored")
	}
	// host -> phone with push
	e.do("POST", base+"phone", e.hostT, protocol.InboxPost{Box: "BBBB", Push: true}, nil)
	if m := e.push.last(); m["type"] != "deck" || m["token"] != "tokD" {
		t.Fatalf("deck push %v", m)
	}
	e.do("GET", base+"phone?after=0", e.phoneT, nil, &got)
	if len(got.Messages) != 1 || got.Messages[0].Box != "BBBB" {
		t.Fatalf("phone inbox %+v", got)
	}
}

func TestInboxLongPollWakesUp(t *testing.T) {
	e := newEnv(t).pairDeck()
	base := "/v1/channels/" + e.ch + "/inbox/"
	done := make(chan protocol.InboxResp, 1)
	go func() {
		var got protocol.InboxResp
		e.do("GET", base+"host?after=0&wait=5", e.hostT, nil, &got)
		done <- got
	}()
	time.Sleep(100 * time.Millisecond)
	e.do("POST", base+"host", e.phoneT, protocol.InboxPost{Box: "CCCC"}, nil)
	select {
	case got := <-done:
		if len(got.Messages) != 1 {
			t.Fatalf("woke with %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll did not wake up")
	}
}

func TestInboxLimits(t *testing.T) {
	e := newEnv(t).pairDeck()
	big := strings.Repeat("A", 200<<10)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/inbox/host", e.phoneT, protocol.InboxPost{Box: big}, nil); c != 413 && c != 400 {
		t.Fatalf("oversized message accepted: %d", c)
	}
}

func TestBlobs(t *testing.T) {
	e := newEnv(t).pairDeck()
	data := bytes.Repeat([]byte("png"), 300_000) // ~900 KB
	req, _ := http.NewRequest("PUT", e.srv.URL+"/v1/channels/"+e.ch+"/blobs", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+e.hostT)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("upload: %v %v", err, resp.Status)
	}
	var up protocol.BlobResp
	jsonDecode(t, resp.Body, &up)
	req, _ = http.NewRequest("GET", e.srv.URL+"/v1/channels/"+e.ch+"/blobs/"+up.ID, nil)
	req.Header.Set("Authorization", "Bearer "+e.phoneT)
	resp, _ = http.DefaultClient.Do(req)
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download: %d len %d", resp.StatusCode, len(got))
	}
	req, _ = http.NewRequest("GET", e.srv.URL+"/v1/channels/"+e.ch+"/blobs/../channels.json", nil)
	req.Header.Set("Authorization", "Bearer "+e.phoneT)
	if resp, _ = http.DefaultClient.Do(req); resp.StatusCode == 200 {
		t.Fatal("path traversal on blobs")
	}
}

func TestConfigPerApp(t *testing.T) {
	p := &fakePusher{}
	r, _ := New(Options{DataDir: t.TempDir(), Pusher: p, Logger: quiet,
		FCMConfig: map[string]string{"project_id": "p", "app_id": "sudo-app"}, FCMAppIDs: map[string]string{"deck": "deck-app"}})
	e := &env{t: t}
	e.srv = newServer(t, r)
	var cfg protocol.RelayConfig
	e.do("GET", "/v1/config?app=deck", "", nil, &cfg)
	if cfg.FCM["app_id"] != "deck-app" || cfg.FCM["project_id"] != "p" {
		t.Fatalf("deck config %+v", cfg)
	}
	e.do("GET", "/v1/config", "", nil, &cfg)
	if cfg.FCM["app_id"] != "sudo-app" {
		t.Fatalf("default config %+v", cfg)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func jsonDecode(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func newServer(t *testing.T, h http.Handler) *httptest.Server {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// Clients keep their "after" cursor across relay restarts; sequence numbers
// must keep growing or every later message is invisible (happened 24.11.2026).
func TestInboxSeqGrowsAcrossRestart(t *testing.T) {
	e := newEnv(t).pairDeck()
	var first protocol.InboxPostResp
	e.do("POST", "/v1/channels/"+e.ch+"/inbox/host", e.phoneT, protocol.InboxPost{Box: "A"}, &first)
	r2, err := New(Options{DataDir: e.relay.opts.DataDir, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Config.Handler = r2
	var second protocol.InboxPostResp
	e.do("POST", "/v1/channels/"+e.ch+"/inbox/host", e.phoneT, protocol.InboxPost{Box: "B"}, &second)
	if second.Seq <= first.Seq {
		t.Fatalf("seq went backwards after restart: %d -> %d", first.Seq, second.Seq)
	}
	var got protocol.InboxResp
	e.do("GET", "/v1/channels/"+e.ch+"/inbox/host?after="+itoa(first.Seq), e.hostT, nil, &got)
	if len(got.Messages) != 1 || got.Messages[0].Box != "B" {
		t.Fatalf("message after restart not delivered: %+v", got)
	}
}
