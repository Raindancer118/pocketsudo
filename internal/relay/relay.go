// Package relay is the internet-facing rendezvous between notebook and phone.
// It stores pairings, wakes the phone via push and hands decisions back to
// the waiting host. It never holds a key that could approve anything.
package relay

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Raindancer118/pocketsudo/protocol"
)

// Pusher wakes a phone. data must not contain anything sensitive; the phone
// fetches the signed request from the relay itself.
type Pusher interface {
	Push(ctx context.Context, token string, data map[string]string, ttl time.Duration) error
}

type Options struct {
	DataDir    string
	Pusher     Pusher            // nil: no push, phone has to poll
	FCMConfig  map[string]string // public Firebase client options handed to the app
	FCMAppIDs  map[string]string // per-app Firebase app ids, e.g. "deck" -> 1:…:android:…
	TurnSecret string            // coturn static-auth-secret; empty disables /turn
	TurnHost   string
	TrustProxy bool // take the client IP from X-Real-IP / X-Forwarded-For
	Logger     *slog.Logger
}

const (
	pairingWindow  = 10 * time.Minute
	maxWait        = 30 * time.Second
	maxBody        = 16 << 10
	maxChannels    = 2000
	maxPendingPerC = 20
	maxRequestTTL  = 5 * time.Minute

	inboxTTL     = 10 * time.Minute
	inboxMax     = 200
	maxInboxBody = 160 << 10
	blobTTL      = 15 * time.Minute
	maxBlob      = 25 << 20
	maxBlobsPerC = 20
)

type channel struct {
	ID             string    `json:"id"`
	HostPub        string    `json:"host_pub"`
	Hostname       string    `json:"hostname"`
	HostTokenHash  string    `json:"host_token_hash"`
	PhoneTokenHash string    `json:"phone_token_hash,omitempty"`
	PhonePub       string    `json:"phone_pub,omitempty"`
	DeviceName     string    `json:"device_name,omitempty"`
	PairMAC        string    `json:"pair_mac,omitempty"`
	FCMToken       string    `json:"fcm_token,omitempty"`
	State          string    `json:"state"`
	App            string    `json:"app,omitempty"`
	HostKX         string    `json:"host_kx,omitempty"`
	PhoneKX        string    `json:"phone_kx,omitempty"`
	Created        time.Time `json:"created"`
	LastSeen       time.Time `json:"last_seen"`
}

type pending struct {
	channel  string
	id       string
	signed   protocol.SignedRequest
	expires  time.Time
	state    string
	response *protocol.Response
	changed  chan struct{} // closed and replaced on every state change
}

type Server struct {
	opts     Options
	log      *slog.Logger
	mux      *http.ServeMux
	mu       sync.Mutex
	channels map[string]*channel
	requests map[string]*pending // key channel/id
	pairWait map[string]chan struct{}
	limiter  *limiter
	inboxes  map[string]*inbox // channel/host or channel/phone
}

type inboxMsg struct {
	seq int64
	box string
	at  time.Time
}

type inbox struct {
	msgs    []inboxMsg
	next    int64
	changed chan struct{}
}

func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{opts: opts, log: opts.Logger, channels: map[string]*channel{},
		requests: map[string]*pending{}, pairWait: map[string]chan struct{}{}, limiter: newLimiter(20, time.Minute),
		inboxes: map[string]*inbox{}}
	if err := os.MkdirAll(filepath.Join(opts.DataDir, "blobs"), 0o700); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// Fail loudly now instead of pairing in memory and losing it on restart.
	if err := checkWritable(opts.DataDir); err != nil {
		return nil, err
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	m.HandleFunc("GET /v1/config", s.config)
	m.HandleFunc("POST /v1/channels", s.createChannel)
	m.HandleFunc("GET /v1/channels/{c}/host", s.hostInfo)
	m.HandleFunc("POST /v1/channels/{c}/phone", s.registerPhone)
	m.HandleFunc("GET /v1/channels/{c}/pairing", s.withHost(s.pairingInfo))
	m.HandleFunc("POST /v1/channels/{c}/confirm", s.withHost(s.confirm))
	m.HandleFunc("GET /v1/channels/{c}/status", s.withPhone(s.status))
	m.HandleFunc("PUT /v1/channels/{c}/fcm", s.withPhone(s.updateFCM))
	m.HandleFunc("DELETE /v1/channels/{c}", s.withAny(s.deleteChannel))
	m.HandleFunc("POST /v1/channels/{c}/requests", s.withHost(s.submit))
	m.HandleFunc("GET /v1/channels/{c}/requests", s.withPhone(s.listPending))
	m.HandleFunc("GET /v1/channels/{c}/requests/{id}", s.withPhone(s.getRequest))
	m.HandleFunc("POST /v1/channels/{c}/requests/{id}/seen", s.withPhone(s.seen))
	m.HandleFunc("POST /v1/channels/{c}/requests/{id}/response", s.withPhone(s.respond))
	m.HandleFunc("GET /v1/channels/{c}/requests/{id}/response", s.withHost(s.waitResponse))
	m.HandleFunc("DELETE /v1/channels/{c}/requests/{id}", s.withHost(s.cancel))
	m.HandleFunc("POST /v1/channels/{c}/inbox/{box}", s.withAny(s.inboxPost))
	m.HandleFunc("GET /v1/channels/{c}/inbox/{box}", s.withAny(s.inboxGet))
	m.HandleFunc("PUT /v1/channels/{c}/blobs", s.withAny(s.blobPut))
	m.HandleFunc("GET /v1/channels/{c}/turn", s.withAny(s.turn))
	m.HandleFunc("GET /v1/channels/{c}/blobs/{id}", s.withAny(s.blobGet))
	s.mux = m
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	limit := int64(maxBody)
	switch {
	case strings.Contains(r.URL.Path, "/blobs"):
		limit = maxBlob
	case strings.Contains(r.URL.Path, "/inbox/"):
		limit = maxInboxBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

// Janitor drops expired requests and abandoned pairings until ctx ends.
func (s *Server) Janitor(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(time.Now())
		}
	}
}

func (s *Server) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.requests {
		if now.After(p.expires.Add(time.Minute)) {
			delete(s.requests, k)
		} else if now.After(p.expires) && (p.state == protocol.ReqPending || p.state == protocol.ReqSeen) {
			s.setState(p, protocol.ReqExpired)
		}
	}
	for _, ib := range s.inboxes {
		keep := ib.msgs[:0]
		for _, m := range ib.msgs {
			if now.Sub(m.at) < inboxTTL {
				keep = append(keep, m)
			}
		}
		ib.msgs = keep
	}
	if entries, err := os.ReadDir(filepath.Join(s.opts.DataDir, "blobs")); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > blobTTL {
				os.Remove(filepath.Join(s.opts.DataDir, "blobs", e.Name()))
			}
		}
	}
	dirty := false
	for id, c := range s.channels {
		if c.State != protocol.StateActive && now.Sub(c.Created) > pairingWindow {
			delete(s.channels, id)
			dirty = true
		}
	}
	if dirty {
		s.saveLocked()
	}
}

// ---- persistence ----

func (s *Server) file() string { return filepath.Join(s.opts.DataDir, "channels.json") }

func (s *Server) load() error {
	data, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*channel
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, c := range list {
		s.channels[c.ID] = c
	}
	return nil
}

func (s *Server) saveLocked() {
	list := make([]*channel, 0, len(s.channels))
	for _, c := range s.channels {
		list = append(list, c)
	}
	data, _ := json.MarshalIndent(list, "", " ")
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		s.log.Error("save channels", "err", err)
		return
	}
	if err := os.Rename(tmp, s.file()); err != nil {
		s.log.Error("save channels", "err", err)
	}
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("data dir %s not writable: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// ---- helpers ----

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func tokenOK(hash, token string) bool {
	if hash == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hash), []byte(hashToken(token))) == 1
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func (s *Server) clientIP(r *http.Request) string {
	if s.opts.TrustProxy {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func waitParam(r *http.Request) time.Duration {
	n, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	d := time.Duration(n) * time.Second
	if d < 0 {
		return 0
	}
	return min(d, maxWait)
}

type authed func(w http.ResponseWriter, r *http.Request, c *channel)

func (s *Server) withRole(role string, next authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		c := s.channels[r.PathValue("c")]
		ok := false
		if c != nil {
			t := bearer(r)
			switch role {
			case "host":
				ok = tokenOK(c.HostTokenHash, t)
			case "phone":
				ok = tokenOK(c.PhoneTokenHash, t)
			default:
				ok = tokenOK(c.HostTokenHash, t) || tokenOK(c.PhoneTokenHash, t)
			}
			if ok && role == "phone" {
				c.LastSeen = time.Now()
			}
		}
		s.mu.Unlock()
		if !ok {
			fail(w, http.StatusUnauthorized, "unknown channel or bad token")
			return
		}
		next(w, r, c)
	}
}

func (s *Server) withHost(n authed) http.HandlerFunc  { return s.withRole("host", n) }
func (s *Server) withPhone(n authed) http.HandlerFunc { return s.withRole("phone", n) }
func (s *Server) withAny(n authed) http.HandlerFunc   { return s.withRole("any", n) }

// ---- pairing ----

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]string{}
	for k, v := range s.opts.FCMConfig {
		cfg[k] = v
	}
	if id, ok := s.opts.FCMAppIDs[r.URL.Query().Get("app")]; ok && id != "" {
		cfg["app_id"] = id
	}
	if len(cfg) == 0 {
		cfg = nil
	}
	writeJSON(w, 200, protocol.RelayConfig{FCM: cfg})
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(s.clientIP(r)) {
		fail(w, http.StatusTooManyRequests, "slow down")
		return
	}
	var req protocol.CreateChannelReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad json")
		return
	}
	if _, err := protocol.DecodePublicKey(req.HostPub); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(req.Hostname) > 128 {
		fail(w, 400, "hostname too long")
		return
	}
	switch req.App {
	case "", protocol.AppSudo:
		req.App = protocol.AppSudo
	case protocol.AppDeck:
		if _, err := protocol.DecodeKXPublic(req.HostKX); err != nil {
			fail(w, 400, "deck channel needs host_kx")
			return
		}
	default:
		fail(w, 400, "unknown app")
		return
	}
	token := protocol.RandomID(32)
	c := &channel{ID: protocol.RandomID(16), HostPub: req.HostPub, Hostname: req.Hostname,
		HostTokenHash: hashToken(token), State: protocol.StatePairing, Created: time.Now(), App: req.App, HostKX: req.HostKX}
	s.mu.Lock()
	if len(s.channels) >= maxChannels {
		s.mu.Unlock()
		fail(w, http.StatusServiceUnavailable, "relay full")
		return
	}
	s.channels[c.ID] = c
	s.saveLocked()
	s.mu.Unlock()
	s.log.Info("channel created", "channel", c.ID[:8], "hostname", c.Hostname)
	writeJSON(w, 201, protocol.CreateChannelResp{Channel: c.ID, HostToken: token})
}

func (s *Server) hostInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.channels[r.PathValue("c")]
	var info protocol.HostInfo
	if c != nil {
		info = protocol.HostInfo{HostPub: c.HostPub, Hostname: c.Hostname, State: c.State, App: c.App, HostKX: c.HostKX}
	}
	s.mu.Unlock()
	if c == nil {
		fail(w, 404, "unknown channel")
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) notifyPairing(id string) {
	if ch, ok := s.pairWait[id]; ok {
		close(ch)
		delete(s.pairWait, id)
	}
}

func (s *Server) pairChanLocked(id string) chan struct{} {
	ch, ok := s.pairWait[id]
	if !ok {
		ch = make(chan struct{})
		s.pairWait[id] = ch
	}
	return ch
}

func (s *Server) registerPhone(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(s.clientIP(r)) {
		fail(w, http.StatusTooManyRequests, "slow down")
		return
	}
	var req protocol.RegisterPhoneReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad json")
		return
	}
	if _, err := protocol.DecodePublicKey(req.PhonePub); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if req.PhoneKX != "" {
		if _, err := protocol.DecodeKXPublic(req.PhoneKX); err != nil {
			fail(w, 400, "phone_kx: "+err.Error())
			return
		}
	}
	if len(req.DeviceName) > 128 || len(req.FCMToken) > 4096 || len(req.MAC) > 128 {
		fail(w, 400, "field too long")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.channels[r.PathValue("c")]
	if c == nil {
		fail(w, 404, "unknown channel")
		return
	}
	if c.State != protocol.StatePairing {
		fail(w, http.StatusConflict, "channel already claimed")
		return
	}
	token := protocol.RandomID(32)
	if c.App == protocol.AppDeck && req.PhoneKX == "" {
		fail(w, 400, "deck channel needs phone_kx")
		return
	}
	c.PhonePub, c.PhoneKX, c.DeviceName, c.FCMToken, c.PairMAC = req.PhonePub, req.PhoneKX, req.DeviceName, req.FCMToken, req.MAC
	c.PhoneTokenHash = hashToken(token)
	c.State = protocol.StateClaimed
	c.LastSeen = time.Now()
	s.saveLocked()
	s.notifyPairing(c.ID)
	writeJSON(w, 201, protocol.RegisterPhoneResp{PhoneToken: token})
}

func (s *Server) pairingInfo(w http.ResponseWriter, r *http.Request, c *channel) {
	deadline := time.After(waitParam(r))
	for {
		s.mu.Lock()
		if c.State != protocol.StatePairing {
			info := protocol.PairingInfo{PhonePub: c.PhonePub, PhoneKX: c.PhoneKX, DeviceName: c.DeviceName, MAC: c.PairMAC}
			s.mu.Unlock()
			writeJSON(w, 200, info)
			return
		}
		ch := s.pairChanLocked(c.ID)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			w.WriteHeader(204)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request, c *channel) {
	var req protocol.ConfirmReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad json")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.State != protocol.StateClaimed {
		fail(w, http.StatusConflict, "no phone waiting")
		return
	}
	if req.Accept {
		c.State = protocol.StateActive
		c.PairMAC = ""
	} else {
		c.State = protocol.StatePairing
		c.PhonePub, c.PhoneKX, c.DeviceName, c.FCMToken, c.PairMAC, c.PhoneTokenHash = "", "", "", "", "", ""
	}
	s.saveLocked()
	s.notifyPairing(c.ID)
	s.log.Info("pairing decided", "channel", c.ID[:8], "accept", req.Accept)
	w.WriteHeader(204)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, c *channel) {
	deadline := time.After(waitParam(r))
	for {
		s.mu.Lock()
		st := c.State
		if st != protocol.StateClaimed {
			s.mu.Unlock()
			writeJSON(w, 200, protocol.ChannelStatus{State: st})
			return
		}
		ch := s.pairChanLocked(c.ID)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			writeJSON(w, 200, protocol.ChannelStatus{State: st})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) updateFCM(w http.ResponseWriter, r *http.Request, c *channel) {
	var req protocol.FCMTokenReq
	if err := readJSON(r, &req); err != nil || len(req.FCMToken) > 4096 {
		fail(w, 400, "bad json")
		return
	}
	s.mu.Lock()
	c.FCMToken = req.FCMToken
	s.saveLocked()
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, c *channel) {
	s.mu.Lock()
	delete(s.channels, c.ID)
	delete(s.inboxes, c.ID+"/host")
	delete(s.inboxes, c.ID+"/phone")
	if entries, err := os.ReadDir(filepath.Join(s.opts.DataDir, "blobs")); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), c.ID+"_") {
				os.Remove(filepath.Join(s.opts.DataDir, "blobs", e.Name()))
			}
		}
	}
	for k, p := range s.requests {
		if p.channel == c.ID {
			s.setState(p, protocol.ReqCancelled)
			delete(s.requests, k)
		}
	}
	s.saveLocked()
	s.mu.Unlock()
	s.log.Info("channel deleted", "channel", c.ID[:8])
	w.WriteHeader(204)
}

// ---- requests ----

func (s *Server) setState(p *pending, st string) {
	p.state = st
	close(p.changed)
	p.changed = make(chan struct{})
}

func (s *Server) push(c *channel, data map[string]string, ttl time.Duration) string {
	if s.opts.Pusher == nil || c.FCMToken == "" {
		return "none"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.opts.Pusher.Push(ctx, c.FCMToken, data, ttl); err != nil {
		s.log.Warn("push failed", "channel", c.ID[:8], "err", err)
		return "failed"
	}
	return "sent"
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, c *channel) {
	var sr protocol.SignedRequest
	if err := readJSON(r, &sr); err != nil {
		fail(w, 400, "bad json")
		return
	}
	s.mu.Lock()
	active, hostPub := c.State == protocol.StateActive, c.HostPub
	s.mu.Unlock()
	if !active {
		fail(w, http.StatusConflict, "channel not paired")
		return
	}
	pub, err := protocol.DecodePublicKey(hostPub)
	if err != nil {
		fail(w, 500, "stored host key broken")
		return
	}
	req, err := protocol.VerifyRequest(pub, sr)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	now := time.Now()
	exp := time.Unix(req.Expires, 0)
	if req.Channel != c.ID || len(req.ID) < 16 || len(req.ID) > 64 || !exp.After(now) || exp.Sub(now) > maxRequestTTL {
		fail(w, 400, "request id, channel or expiry invalid")
		return
	}
	key := c.ID + "/" + req.ID
	s.mu.Lock()
	n := 0
	for _, p := range s.requests {
		if p.channel == c.ID && (p.state == protocol.ReqPending || p.state == protocol.ReqSeen) {
			n++
		}
	}
	if _, dup := s.requests[key]; dup || n >= maxPendingPerC {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "duplicate request or too many pending")
		return
	}
	s.requests[key] = &pending{channel: c.ID, id: req.ID, signed: sr, expires: exp,
		state: protocol.ReqPending, changed: make(chan struct{})}
	s.mu.Unlock()

	res := s.push(c, map[string]string{"type": "request", "channel": c.ID, "id": req.ID}, exp.Sub(now))
	s.log.Info("request submitted", "channel", c.ID[:8], "push", res)
	writeJSON(w, 201, protocol.SubmitResp{Push: res})
}

func (s *Server) lookup(c *channel, id string) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[c.ID+"/"+id]
}

func (s *Server) listPending(w http.ResponseWriter, _ *http.Request, c *channel) {
	now := time.Now()
	out := []protocol.SignedRequest{}
	s.mu.Lock()
	for _, p := range s.requests {
		if p.channel == c.ID && (p.state == protocol.ReqPending || p.state == protocol.ReqSeen) && p.expires.After(now) {
			out = append(out, p.signed)
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request, c *channel) {
	p := s.lookup(c, r.PathValue("id"))
	if p == nil {
		fail(w, 404, "unknown request")
		return
	}
	s.mu.Lock()
	body := struct {
		protocol.SignedRequest
		State string `json:"state"`
	}{p.signed, p.state}
	s.mu.Unlock()
	writeJSON(w, 200, body)
}

func (s *Server) seen(w http.ResponseWriter, r *http.Request, c *channel) {
	p := s.lookup(c, r.PathValue("id"))
	if p == nil {
		fail(w, 404, "unknown request")
		return
	}
	s.mu.Lock()
	if p.state == protocol.ReqPending {
		s.setState(p, protocol.ReqSeen)
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, c *channel) {
	var resp protocol.Response
	if err := readJSON(r, &resp); err != nil {
		fail(w, 400, "bad json")
		return
	}
	p := s.lookup(c, r.PathValue("id"))
	if p == nil {
		fail(w, http.StatusGone, "request no longer exists")
		return
	}
	// A denial needs no signature: the phone key only signs after a
	// fingerprint, and saying no should not require one. A forged deny gains
	// nothing the relay could not already do by dropping the request.
	if resp.Decision == protocol.DecisionDeny {
		resp.Sig = ""
	} else if pub, err := protocol.DecodePublicKey(c.PhonePub); err != nil || !protocol.VerifyResponse(pub, p.signed.Payload, resp) {
		fail(w, 400, "bad phone signature")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p.state == protocol.ReqAnswered:
		fail(w, http.StatusConflict, "already answered")
		return
	case p.state == protocol.ReqCancelled, p.state == protocol.ReqExpired, time.Now().After(p.expires):
		fail(w, http.StatusGone, "request cancelled or expired")
		return
	}
	p.response = &resp
	s.setState(p, protocol.ReqAnswered)
	s.log.Info("request answered", "channel", c.ID[:8], "decision", resp.Decision)
	w.WriteHeader(204)
}

// waitResponse long-polls until the state differs from ?known= (default pending).
func (s *Server) waitResponse(w http.ResponseWriter, r *http.Request, c *channel) {
	p := s.lookup(c, r.PathValue("id"))
	if p == nil {
		writeJSON(w, 200, protocol.RequestState{State: protocol.ReqExpired})
		return
	}
	known := r.URL.Query().Get("known")
	if known == "" {
		known = protocol.ReqPending
	}
	deadline := time.After(waitParam(r))
	for {
		s.mu.Lock()
		st, resp, ch := p.state, p.response, p.changed
		s.mu.Unlock()
		if st != known {
			writeJSON(w, 200, protocol.RequestState{State: st, Response: resp})
			return
		}
		select {
		case <-ch:
		case <-deadline:
			writeJSON(w, 200, protocol.RequestState{State: st})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, c *channel) {
	p := s.lookup(c, r.PathValue("id"))
	if p == nil {
		w.WriteHeader(204)
		return
	}
	s.mu.Lock()
	wasOpen := p.state == protocol.ReqPending || p.state == protocol.ReqSeen
	if wasOpen {
		s.setState(p, protocol.ReqCancelled)
	}
	s.mu.Unlock()
	if wasOpen {
		s.push(c, map[string]string{"type": "cancel", "channel": c.ID, "id": p.id}, time.Minute)
	}
	w.WriteHeader(204)
}

// ---- rate limiting ----

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			h = append(h, t)
		}
	}
	if len(h) >= l.max {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 {
		l.hits = map[string][]time.Time{}
	}
	return true
}

// ---- deck mailbox ----

// role returns "host" or "phone" for the token used on this request.
func (s *Server) role(r *http.Request, c *channel) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tokenOK(c.HostTokenHash, bearer(r)) {
		return "host"
	}
	return "phone"
}

func (s *Server) inboxLocked(key string) *inbox {
	ib, ok := s.inboxes[key]
	if !ok {
		ib = &inbox{changed: make(chan struct{})}
		s.inboxes[key] = ib
	}
	return ib
}

func (s *Server) inboxPost(w http.ResponseWriter, r *http.Request, c *channel) {
	box := r.PathValue("box")
	sender := s.role(r, c)
	// you only ever write into the other side's inbox
	if (box != "host" && box != "phone") || box == sender {
		fail(w, http.StatusUnauthorized, "wrong inbox for this token")
		return
	}
	var req protocol.InboxPost
	if err := readJSON(r, &req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, http.StatusRequestEntityTooLarge, "message too large")
			return
		}
		fail(w, 400, "bad json")
		return
	}
	if req.Box == "" {
		fail(w, 400, "empty box")
		return
	}
	s.mu.Lock()
	if c.State != protocol.StateActive {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "channel not paired")
		return
	}
	ib := s.inboxLocked(c.ID + "/" + box)
	// time-based so the numbers keep growing across relay restarts
	ib.next = max(ib.next+1, time.Now().UnixMicro())
	ib.msgs = append(ib.msgs, inboxMsg{seq: ib.next, box: req.Box, at: time.Now()})
	if len(ib.msgs) > inboxMax {
		ib.msgs = ib.msgs[len(ib.msgs)-inboxMax:]
	}
	seq := ib.next
	close(ib.changed)
	ib.changed = make(chan struct{})
	s.mu.Unlock()
	if req.Push && box == "phone" {
		s.push(c, map[string]string{"type": "deck", "channel": c.ID}, time.Minute)
	}
	writeJSON(w, 201, protocol.InboxPostResp{Seq: seq})
}

func (s *Server) inboxGet(w http.ResponseWriter, r *http.Request, c *channel) {
	box := r.PathValue("box")
	if box != s.role(r, c) {
		fail(w, http.StatusUnauthorized, "not your inbox")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	deadline := time.After(waitParam(r))
	for {
		s.mu.Lock()
		ib := s.inboxLocked(c.ID + "/" + box)
		out := []protocol.InboxMessage{}
		for _, m := range ib.msgs {
			if m.seq > after {
				out = append(out, protocol.InboxMessage{Seq: m.seq, Box: m.box})
			}
		}
		ch := ib.changed
		s.mu.Unlock()
		if len(out) > 0 {
			writeJSON(w, 200, protocol.InboxResp{Messages: out})
			return
		}
		select {
		case <-ch:
		case <-deadline:
			writeJSON(w, 200, protocol.InboxResp{Messages: out})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) blobPath(c *channel, id string) (string, bool) {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", false
	}
	return filepath.Join(s.opts.DataDir, "blobs", c.ID+"_"+id), true
}

func (s *Server) blobPut(w http.ResponseWriter, r *http.Request, c *channel) {
	dir := filepath.Join(s.opts.DataDir, "blobs")
	if entries, err := os.ReadDir(dir); err == nil {
		n := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), c.ID+"_") {
				n++
			}
		}
		if n >= maxBlobsPerC {
			fail(w, http.StatusTooManyRequests, "too many files waiting")
			return
		}
	}
	id := protocol.RandomID(16)
	path, _ := s.blobPath(c, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fail(w, 500, "store failed")
		return
	}
	_, err = io.Copy(f, r.Body)
	f.Close()
	if err != nil {
		os.Remove(path)
		fail(w, http.StatusRequestEntityTooLarge, "file too large or upload broken")
		return
	}
	writeJSON(w, 201, protocol.BlobResp{ID: id})
}

func (s *Server) blobGet(w http.ResponseWriter, r *http.Request, c *channel) {
	path, ok := s.blobPath(c, r.PathValue("id"))
	if !ok {
		fail(w, 404, "unknown file")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, 404, "unknown file")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

const turnTTL = time.Hour

// turn hands paired devices short-lived TURN credentials (coturn REST
// scheme), so the TURN server is no open proxy and no password ships in an app.
func (s *Server) turn(w http.ResponseWriter, _ *http.Request, c *channel) {
	if s.opts.TurnSecret == "" || s.opts.TurnHost == "" {
		fail(w, http.StatusNotFound, "no TURN server configured")
		return
	}
	user := strconv.FormatInt(time.Now().Add(turnTTL).Unix(), 10) + ":" + c.ID[:8]
	m := hmac.New(sha1.New, []byte(s.opts.TurnSecret))
	m.Write([]byte(user))
	host := s.opts.TurnHost
	writeJSON(w, 200, protocol.TurnCreds{
		URLs:     []string{"turn:" + host + ":3478?transport=udp", "turn:" + host + ":3478?transport=tcp", "stun:" + host + ":3478"},
		Username: user, Credential: base64.StdEncoding.EncodeToString(m.Sum(nil)), TTL: int(turnTTL.Seconds()),
	})
}
