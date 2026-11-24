package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/protocol"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakePusher struct {
	mu   sync.Mutex
	msgs []map[string]string
}

func (f *fakePusher) Push(_ context.Context, token string, data map[string]string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := map[string]string{"token": token}
	for k, v := range data {
		d[k] = v
	}
	f.msgs = append(f.msgs, d)
	return nil
}

func (f *fakePusher) last() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return nil
	}
	return f.msgs[len(f.msgs)-1]
}

type env struct {
	t      *testing.T
	srv    *httptest.Server
	push   *fakePusher
	relay  *Server
	host   *ecdsa.PrivateKey
	phone  *ecdsa.PrivateKey
	ch     string
	hostT  string
	phoneT string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	p := &fakePusher{}
	r, err := New(Options{DataDir: t.TempDir(), Pusher: p, Logger: quiet, FCMConfig: map[string]string{"project_id": "p"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	h, _ := protocol.GenerateKey()
	ph, _ := protocol.GenerateKey()
	return &env{t: t, srv: srv, push: p, relay: r, host: h, phone: ph}
}

func (e *env) do(method, path, token string, body any, out any) int {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 && resp.StatusCode != 204 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// pair runs the full pairing handshake and returns the env ready for requests.
func (e *env) pair() *env {
	e.t.Helper()
	hostPub, _ := protocol.EncodePublicKey(&e.host.PublicKey)
	var created protocol.CreateChannelResp
	if c := e.do("POST", "/v1/channels", "", map[string]string{"host_pub": hostPub, "hostname": "eclipse"}, &created); c != 201 {
		e.t.Fatalf("create channel: %d", c)
	}
	e.ch, e.hostT = created.Channel, created.HostToken
	var info protocol.HostInfo
	e.do("GET", "/v1/channels/"+e.ch+"/host", "", nil, &info)
	if info.HostPub != hostPub || info.State != "pairing" {
		e.t.Fatalf("host info %+v", info)
	}
	phonePub, _ := protocol.EncodePublicKey(&e.phone.PublicKey)
	secret := []byte("secretsecretsecretsecretsecret12")
	var reg protocol.RegisterPhoneResp
	body := map[string]string{"phone_pub": phonePub, "device_name": "S24 FE", "fcm_token": "tok1",
		"mac": protocol.PairMAC(secret, e.ch, phonePub, "S24 FE")}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/phone", "", body, &reg); c != 201 {
		e.t.Fatalf("register phone: %d", c)
	}
	e.phoneT = reg.PhoneToken
	if c := e.do("POST", "/v1/channels/"+e.ch+"/phone", "", body, nil); c != 409 {
		e.t.Fatalf("second phone registration should be rejected, got %d", c)
	}
	var pairing protocol.PairingInfo
	if c := e.do("GET", "/v1/channels/"+e.ch+"/pairing?wait=1", e.hostT, nil, &pairing); c != 200 {
		e.t.Fatalf("pairing poll: %d", c)
	}
	if !protocol.CheckPairMAC(secret, e.ch, pairing.PhonePub, pairing.DeviceName, pairing.MAC) {
		e.t.Fatal("mac did not survive the relay")
	}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/confirm", e.hostT, map[string]bool{"accept": true}, nil); c != 204 {
		e.t.Fatalf("confirm: %d", c)
	}
	var st protocol.ChannelStatus
	e.do("GET", "/v1/channels/"+e.ch+"/status", e.phoneT, nil, &st)
	if st.State != "active" {
		e.t.Fatalf("state after confirm: %s", st.State)
	}
	return e
}

func (e *env) newRequest(expiresIn int64) protocol.SignedRequest {
	r := protocol.Request{V: 1, ID: protocol.RandomID(16), Channel: e.ch, Hostname: "eclipse", User: "tom",
		Service: "sudo", Command: "true", Code: "07", Created: protocol.Now(),
		Expires: protocol.Now() + expiresIn, Nonce: protocol.RandomID(16)}
	sr, err := protocol.SignRequest(e.host, r)
	if err != nil {
		e.t.Fatal(err)
	}
	return sr
}

func requestID(t *testing.T, sr protocol.SignedRequest) string {
	var r protocol.Request
	b, _ := decodeB64(sr.Payload)
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestFullApprovalFlow(t *testing.T) {
	e := newEnv(t).pair()
	sr := e.newRequest(60)
	id := requestID(t, sr)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, sr, nil); c != 201 {
		t.Fatalf("submit: %d", c)
	}
	if m := e.push.last(); m["type"] != "request" || m["id"] != id || m["token"] != "tok1" || m["channel"] != e.ch {
		t.Fatalf("push not sent correctly: %v", m)
	}

	type state = protocol.RequestState
	var pending []protocol.SignedRequest
	e.do("GET", "/v1/channels/"+e.ch+"/requests", e.phoneT, nil, &pending)
	if len(pending) != 1 || pending[0].Payload != sr.Payload {
		t.Fatalf("pending list %v", pending)
	}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/seen", e.phoneT, nil, nil); c != 204 {
		t.Fatalf("seen: %d", c)
	}
	done := make(chan state, 1)
	go func() {
		var s state
		e.do("GET", "/v1/channels/"+e.ch+"/requests/"+id+"/response?wait=5&known=seen", e.hostT, nil, &s)
		done <- s
	}()

	resp, _ := protocol.SignResponse(e.phone, protocol.DecisionApprove, sr.Payload)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, resp, nil); c != 204 {
		t.Fatalf("respond: %d", c)
	}
	select {
	case s := <-done:
		if s.State != "answered" || s.Response == nil || !protocol.VerifyResponse(&e.phone.PublicKey, sr.Payload, *s.Response) {
			t.Fatalf("host got %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll did not return")
	}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, resp, nil); c != 409 {
		t.Fatalf("second answer should conflict, got %d", c)
	}
}

func TestRelayRejectsGarbageResponses(t *testing.T) {
	e := newEnv(t).pair()
	sr := e.newRequest(60)
	id := requestID(t, sr)
	e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, sr, nil)
	other, _ := protocol.GenerateKey()
	bad, _ := protocol.SignResponse(other, protocol.DecisionApprove, sr.Payload)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, bad, nil); c != 400 {
		t.Fatalf("foreign signature accepted: %d", c)
	}
}

func TestAuthIsEnforced(t *testing.T) {
	e := newEnv(t).pair()
	sr := e.newRequest(60)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests", e.phoneT, sr, nil); c != 401 {
		t.Fatalf("phone token must not submit requests: %d", c)
	}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests", "", sr, nil); c != 401 {
		t.Fatalf("anonymous submit: %d", c)
	}
	if c := e.do("GET", "/v1/channels/"+e.ch+"/requests", e.hostT, nil, nil); c != 401 {
		t.Fatalf("host token must not list as phone: %d", c)
	}
	forged := sr
	forged.Sig = sr.Sig[:10] + "AAAA" + sr.Sig[14:]
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, forged, nil); c != 400 {
		t.Fatalf("badly signed request accepted: %d", c)
	}
}

func TestCancelAndExpiry(t *testing.T) {
	e := newEnv(t).pair()
	sr := e.newRequest(60)
	id := requestID(t, sr)
	e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, sr, nil)
	if c := e.do("DELETE", "/v1/channels/"+e.ch+"/requests/"+id, e.hostT, nil, nil); c != 204 {
		t.Fatalf("cancel: %d", c)
	}
	if m := e.push.last(); m["type"] != "cancel" || m["id"] != id {
		t.Fatalf("cancel push %v", m)
	}
	var pending []protocol.SignedRequest
	e.do("GET", "/v1/channels/"+e.ch+"/requests", e.phoneT, nil, &pending)
	if len(pending) != 0 {
		t.Fatalf("cancelled request still pending")
	}
	resp, _ := protocol.SignResponse(e.phone, protocol.DecisionApprove, sr.Payload)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, resp, nil); c != 410 {
		t.Fatalf("answer to cancelled request: %d", c)
	}

	expired := e.newRequest(-1)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, expired, nil); c != 400 {
		t.Fatalf("expired request accepted: %d", c)
	}
}

func TestChannelsSurviveRestart(t *testing.T) {
	e := newEnv(t).pair()
	dir := e.relay.opts.DataDir
	r2, err := New(Options{DataDir: dir, Pusher: e.push, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Config.Handler = r2
	var st protocol.ChannelStatus
	if c := e.do("GET", "/v1/channels/"+e.ch+"/status", e.phoneT, nil, &st); c != 200 || st.State != "active" {
		t.Fatalf("after restart: %d %s", c, st.State)
	}
	if _, err := filepath.Glob(filepath.Join(dir, "*")); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedPairingCanBeRetried(t *testing.T) {
	e := newEnv(t)
	hostPub, _ := protocol.EncodePublicKey(&e.host.PublicKey)
	var created protocol.CreateChannelResp
	e.do("POST", "/v1/channels", "", map[string]string{"host_pub": hostPub, "hostname": "x"}, &created)
	phonePub, _ := protocol.EncodePublicKey(&e.phone.PublicKey)
	body := map[string]string{"phone_pub": phonePub, "device_name": "evil", "mac": "x"}
	e.do("POST", "/v1/channels/"+created.Channel+"/phone", "", body, nil)
	if c := e.do("POST", "/v1/channels/"+created.Channel+"/confirm", created.HostToken, map[string]bool{"accept": false}, nil); c != 204 {
		t.Fatalf("reject: %d", c)
	}
	body["device_name"] = "S24 FE"
	if c := e.do("POST", "/v1/channels/"+created.Channel+"/phone", "", body, nil); c != 201 {
		t.Fatalf("retry after reject: %d", c)
	}
}

func TestUnpairDeletesChannel(t *testing.T) {
	e := newEnv(t).pair()
	if c := e.do("DELETE", "/v1/channels/"+e.ch, e.phoneT, nil, nil); c != 204 {
		t.Fatalf("unpair: %d", c)
	}
	if c := e.do("GET", "/v1/channels/"+e.ch+"/status", e.phoneT, nil, nil); c != 401 && c != 404 {
		t.Fatalf("channel still usable: %d", c)
	}
}

func TestConfigEndpoint(t *testing.T) {
	e := newEnv(t)
	var cfg protocol.RelayConfig
	e.do("GET", "/v1/config", "", nil, &cfg)
	if cfg.FCM["project_id"] != "p" {
		t.Fatalf("config %+v", cfg)
	}
}

func TestUnsignedDenyIsAccepted(t *testing.T) {
	e := newEnv(t).pair()
	sr := e.newRequest(60)
	id := requestID(t, sr)
	e.do("POST", "/v1/channels/"+e.ch+"/requests", e.hostT, sr, nil)
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, protocol.Response{Decision: protocol.DecisionDeny}, nil); c != 204 {
		t.Fatalf("unsigned deny rejected: %d", c)
	}
	var st protocol.RequestState
	e.do("GET", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.hostT, nil, &st)
	if st.State != protocol.ReqAnswered || st.Response.Decision != protocol.DecisionDeny {
		t.Fatalf("host sees %+v", st)
	}
	if c := e.do("POST", "/v1/channels/"+e.ch+"/requests/"+id+"/response", e.phoneT, protocol.Response{Decision: protocol.DecisionApprove}, nil); c != 400 && c != 409 {
		t.Fatalf("unsigned approve accepted: %d", c)
	}
}

// A relay that cannot persist pairings must refuse to start: otherwise it
// pairs happily and forgets everything on the next restart (happened 24.11.2026).
func TestNewFailsWithoutWritableDataDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	if _, err := New(Options{DataDir: dir, Logger: quiet}); err == nil {
		t.Fatal("relay started without a writable data dir")
	}
}
