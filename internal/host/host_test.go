package host

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/protocol"
	"github.com/Raindancer118/pocketsudo/internal/relay"
)

// fakePhone behaves like the Android app: it scans the QR URI, registers and
// then answers requests with its own key.
type fakePhone struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	relay  string
	ch     string
	token  string
	answer string // approve | deny | "" (ignore)
	seen   chan protocol.Request
}

func (p *fakePhone) scan(uri string) {
	u, err := url.Parse(uri)
	if err != nil {
		p.t.Error(err)
		return
	}
	q := u.Query()
	p.relay, p.ch = q.Get("r"), q.Get("c")
	secret, _ := base64.RawURLEncoding.DecodeString(q.Get("s"))
	c := NewClient(p.relay, "")
	var info protocol.HostInfo
	if _, err := c.Do(context.Background(), "GET", "/v1/channels/"+p.ch+"/host", nil, &info); err != nil {
		p.t.Error(err)
		return
	}
	if fp, _ := protocol.KeyFingerprint(info.HostPub); fp != q.Get("f") {
		p.t.Error("host key fingerprint mismatch")
		return
	}
	pub, _ := protocol.EncodePublicKey(&p.key.PublicKey)
	var reg protocol.RegisterPhoneResp
	_, err = c.Do(context.Background(), "POST", "/v1/channels/"+p.ch+"/phone", protocol.RegisterPhoneReq{
		PhonePub: pub, DeviceName: "Test Phone", MAC: protocol.PairMAC(secret, p.ch, pub, "Test Phone")}, &reg)
	if err != nil {
		p.t.Error(err)
		return
	}
	p.token = reg.PhoneToken
}

func (p *fakePhone) serve(ctx context.Context) {
	c := NewClient(p.relay, p.token)
	handled := map[string]bool{}
	for ctx.Err() == nil {
		var list []protocol.SignedRequest
		if _, err := c.Do(ctx, "GET", "/v1/channels/"+p.ch+"/requests", nil, &list); err == nil {
			for _, sr := range list {
				hostPubInfo := protocol.HostInfo{}
				c.Do(ctx, "GET", "/v1/channels/"+p.ch+"/host", nil, &hostPubInfo)
				hp, _ := protocol.DecodePublicKey(hostPubInfo.HostPub)
				req, err := protocol.VerifyRequest(hp, sr)
				if err != nil || handled[req.ID] {
					continue
				}
				handled[req.ID] = true
				c.Do(ctx, "POST", "/v1/channels/"+p.ch+"/requests/"+req.ID+"/seen", nil, nil)
				if p.seen != nil {
					p.seen <- req
				}
				if p.answer != "" {
					resp, _ := protocol.SignResponse(p.key, p.answer, sr.Payload)
					c.Do(ctx, "POST", "/v1/channels/"+p.ch+"/requests/"+req.ID+"/response", resp, nil)
				}
			}
		}
		sleep(ctx, 50*time.Millisecond)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setup(t *testing.T, answer string) (*Config, *fakePhone, *syncBuf) {
	t.Helper()
	t.Setenv("POCKETSUDO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	r, err := relay.New(relay.Options{DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cfg, _ := LoadConfig()
	phoneKey, _ := protocol.GenerateKey()
	phone := &fakePhone{t: t, key: phoneKey, answer: answer}
	out := &syncBuf{}
	var shownCode string
	_, err = Pair(context.Background(), cfg, srv.URL, "eclipse", PairUI{Out: out,
		ShowQR: phone.scan,
		Confirm: func(device, code string) bool {
			shownCode = code
			return device == "Test Phone"
		}})
	if err != nil {
		t.Fatal(err)
	}
	hostPub, _ := protocol.EncodePublicKey(&mustKey(t, cfg).PublicKey)
	phonePub, _ := protocol.EncodePublicKey(&phoneKey.PublicKey)
	if shownCode != protocol.VerificationCode(hostPub, phonePub) {
		t.Fatal("host showed a verification code the phone would not show")
	}
	reloaded, _ := LoadConfig()
	if len(reloaded.Pairings) != 1 || reloaded.Pairings[0].DeviceName != "Test Phone" {
		t.Fatalf("pairing not persisted: %+v", reloaded.Pairings)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go phone.serve(ctx)
	return reloaded, phone, out
}

func mustKey(t *testing.T, c *Config) *ecdsa.PrivateKey {
	k, err := c.Key()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var sess = Session{Hostname: "eclipse", Service: "sudo", User: "tom", TTY: "/dev/pts/1", Command: "sudo pacman -Syu"}

func TestPhoneApproves(t *testing.T) {
	cfg, phone, _ := setup(t, protocol.DecisionApprove)
	phone.seen = make(chan protocol.Request, 1)
	out := &syncBuf{}
	res := Authenticate(context.Background(), cfg, sess, AuthUI{Out: out}, nil)
	if !res.Approved || res.Via != "Test Phone" {
		t.Fatalf("expected approval, got %+v\n%s", res, out)
	}
	req := <-phone.seen
	if req.Command != "sudo pacman -Syu" || req.User != "tom" || !strings.Contains(out.String(), "code "+req.Code) {
		t.Fatalf("phone saw %+v, terminal:\n%s", req, out)
	}
}

func TestPhoneDenies(t *testing.T) {
	cfg, _, _ := setup(t, protocol.DecisionDeny)
	slowLocal := func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	res := Authenticate(context.Background(), cfg, sess, AuthUI{}, slowLocal)
	if res.Approved || !strings.Contains(res.Reason, "denied") {
		t.Fatalf("expected denial, got %+v", res)
	}
}

func TestLocalFingerprintWinsAndCancelsPhone(t *testing.T) {
	cfg, phone, _ := setup(t, "")
	phone.seen = make(chan protocol.Request, 1)
	local := func(ctx context.Context, user string) error {
		select {
		case <-phone.seen: // wait until the phone has the request, then "touch"
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	res := Authenticate(context.Background(), cfg, sess, AuthUI{}, local)
	if !res.Approved || res.Via != "fingerprint reader" {
		t.Fatalf("got %+v", res)
	}
	var list []protocol.SignedRequest
	NewClient(phone.relay, phone.token).Do(context.Background(), "GET", "/v1/channels/"+phone.ch+"/requests", nil, &list)
	if len(list) != 0 {
		t.Fatal("phone request not cancelled after local approval")
	}
}

func TestSkipAndTimeout(t *testing.T) {
	cfg, _, _ := setup(t, "")
	skip := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(skip) }()
	if res := Authenticate(context.Background(), cfg, sess, AuthUI{Skip: skip}, nil); res.Approved || res.Reason != "skipped" {
		t.Fatalf("skip: %+v", res)
	}
	cfg.TimeoutSec = 1
	start := time.Now()
	if res := Authenticate(context.Background(), cfg, sess, AuthUI{}, nil); res.Approved || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %+v after %v", res, time.Since(start))
	}
}

func TestRemoteSessionSkipsLocalReader(t *testing.T) {
	cfg, _, _ := setup(t, protocol.DecisionApprove)
	called := false
	local := func(ctx context.Context, _ string) error { called = true; return errors.New("no") }
	remote := sess
	remote.Remote = true
	if res := Authenticate(context.Background(), cfg, remote, AuthUI{}, local); !res.Approved || called {
		t.Fatalf("got %+v, local called=%v", res, called)
	}
}

func TestRelayDownFallsBackToLocal(t *testing.T) {
	t.Setenv("POCKETSUDO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg, _ := LoadConfig()
	cfg.Key()
	cfg.Pairings = []Pairing{{Relay: "http://127.0.0.1:1", Channel: "x", HostToken: "y", DeviceName: "Gone"}}
	out := &syncBuf{}
	ok := func(ctx context.Context, _ string) error { return nil }
	res := Authenticate(context.Background(), cfg, sess, AuthUI{Out: out}, ok)
	if !res.Approved || !strings.Contains(out.String(), "Gone unreachable") {
		t.Fatalf("got %+v\n%s", res, out)
	}
}

func TestPairRejectsForgedMAC(t *testing.T) {
	t.Setenv("POCKETSUDO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	r, _ := relay.New(relay.Options{DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	srv := httptest.NewServer(r)
	defer srv.Close()
	cfg, _ := LoadConfig()
	attacker, _ := protocol.GenerateKey()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := &syncBuf{}
	_, err := Pair(ctx, cfg, srv.URL, "eclipse", PairUI{Out: out,
		ShowQR: func(uri string) {
			u, _ := url.Parse(uri)
			ch := u.Query().Get("c")
			pub, _ := protocol.EncodePublicKey(&attacker.PublicKey)
			go NewClient(srv.URL, "").Do(context.Background(), "POST", "/v1/channels/"+ch+"/phone",
				protocol.RegisterPhoneReq{PhonePub: pub, DeviceName: "evil", MAC: "AAAA"}, nil)
		},
		Confirm: func(string, string) bool { t.Error("forged pairing reached confirmation"); return true }})
	if err == nil || len(cfg.Pairings) != 0 || !strings.Contains(out.String(), "rejected") {
		t.Fatalf("err=%v pairings=%d out=%s", err, len(cfg.Pairings), out)
	}
}
