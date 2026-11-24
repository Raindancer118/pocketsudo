package deck

import (
	"context"
	"io"

	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pion/webrtc/v4"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/host"
	"github.com/Raindancer118/pocketsudo/protocol"
)

// System runs desktop commands; tests swap in a recorder.
type System interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	Start(ctx context.Context, name string, args ...string) error // detached, no output
	// Stream runs a long-lived command and calls onLine for each output line.
	Stream(ctx context.Context, onLine func(string), name string, args ...string) error
	// Pipe starts a command and hands over its stdout (video, audio); closing
	// the reader or ending ctx stops the command.
	Pipe(ctx context.Context, name string, args ...string) (io.ReadCloser, error)
}

// Result is the body of every answer to the phone.
type Result struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type Agent struct {
	cfg         *Config
	sys         System
	log         *slog.Logger
	key         []byte
	phonePub    *ecdsa.PublicKey
	client      *host.Client
	guard       *protocol.ReplayGuard
	batteryDir  string
	hostname    string
	downloadDir string
	home        string   // the file browser never leaves this directory
	dataDirs    []string // XDG data dirs: applications, icons, pixmaps
	iconCache   string

	mu           sync.Mutex
	sessionUntil time.Time
	lastClip     string // last text that came from the phone, so it is not echoed back
	stream       *streamer
	iceServers   []webrtc.ICEServer
	velaShell    string // vela's quickshell dir: DND and night light go through it
	velaState    string // its state file; "" = find the newest
	iconIdx      *iconIndex
	iconIdxAt    time.Time
}

func NewAgent(cfg *Config, sys System, log *slog.Logger) (*Agent, error) {
	if !cfg.IsPaired() {
		return nil, errors.New("not paired yet – run: pocketdeck pair")
	}
	kx, err := cfg.kxKey()
	if err != nil {
		return nil, err
	}
	key, err := protocol.DeckKey(kx, cfg.PhoneKX, cfg.Channel)
	if err != nil {
		return nil, err
	}
	pub, err := protocol.DecodePublicKey(cfg.PhonePub)
	if err != nil {
		return nil, err
	}
	hn, _ := os.Hostname()
	return &Agent{cfg: cfg, sys: sys, log: log, key: key, phonePub: pub,
		client: host.NewClient(cfg.Relay, cfg.HostToken), guard: protocol.NewReplayGuard(),
		batteryDir: "/sys/class/power_supply", hostname: hn, downloadDir: defaultDownloadDir(), home: homeDir(), iceServers: defaultICE(),
		dataDirs: defaultDataDirs(), iconCache: defaultIconCache(), velaShell: defaultVelaShell()}, nil
}

// Run long-polls the host inbox until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("pocketdeck agent running", "relay", a.cfg.Relay, "phone", a.cfg.DeviceName)
	go a.watchClipboard(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		var in protocol.InboxResp
		path := fmt.Sprintf("/v1/channels/%s/inbox/host?wait=25&after=%d", a.cfg.Channel, a.cfg.After)
		_, err := a.client.Do(ctx, "GET", path, nil, &in)
		if err != nil {
			var he *host.HTTPError
			if errors.As(err, &he) && he.Code == 401 {
				return errors.New("relay no longer knows this pairing – run: pocketdeck pair")
			}
			if ctx.Err() == nil {
				a.log.Warn("relay", "err", err)
				sleepCtx(ctx, backoff)
				backoff = min(backoff*2, 30*time.Second)
			}
			continue
		}
		backoff = time.Second
		for _, msg := range in.Messages {
			a.cfg.After = msg.Seq
			a.handle(ctx, msg.Box)
		}
		if len(in.Messages) > 0 {
			if err := a.cfg.Save(); err != nil {
				a.log.Warn("save cursor", "err", err)
			}
		}
	}
	return ctx.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (a *Agent) handle(ctx context.Context, box string) {
	m, err := protocol.OpenMessage(a.key, box, time.Now(), a.guard)
	if err != nil {
		// not from our phone, replayed or stale: no answer, nothing runs
		a.log.Warn("dropped message", "err", err)
		return
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := a.dispatch(actx, m)
	res := Result{OK: err == nil, Data: data}
	if err != nil {
		res.Error = err.Error()
		a.log.Info("action failed", "type", m.Type, "err", err)
	} else {
		a.log.Info("action", "type", m.Type)
	}
	a.reply(ctx, m.ID, "result", res)
}

// clipWatchScript prints each new text clipboard as one base64 line. Copies
// that password managers mark as secret are skipped.
const clipWatchScript = `wl-paste --list-types 2>/dev/null | grep -qE 'x-kde-passwordManagerHint|PASSWORD' && exit 0; base64 -w0; echo`

// watchClipboard pushes every new notebook clipboard text to the phone.
func (a *Agent) watchClipboard(ctx context.Context) {
	lastSent := ""
	for ctx.Err() == nil {
		first := true
		err := a.sys.Stream(ctx, func(line string) {
			// wl-paste --watch reports the current content once at start;
			// that is not a new copy and must not wake the phone
			if first {
				first = false
				if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line)); err == nil {
					lastSent = string(raw)
				}
				return
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
			if err != nil || len(raw) == 0 || len(raw) > maxClip {
				return
			}
			text := string(raw)
			a.mu.Lock()
			echo := text == a.lastClip
			if echo {
				a.lastClip = ""
			}
			a.mu.Unlock()
			if echo || text == lastSent {
				return
			}
			lastSent = text
			a.notify(ctx, "clipboard", map[string]string{"text": text})
		}, "wl-paste", "--type", "text", "--watch", "sh", "-c", clipWatchScript)
		if ctx.Err() != nil {
			return
		}
		a.log.Warn("clipboard watcher stopped, restarting", "err", err)
		sleepCtx(ctx, 10*time.Second)
	}
}

// notify sends an unsolicited event to the phone and wakes it via push.
func (a *Agent) notify(ctx context.Context, typ string, body any) {
	b, _ := json.Marshal(body)
	box, err := protocol.SealMessage(a.key, protocol.DeckMessage{ID: protocol.RandomID(16), Type: typ, Body: b}, time.Now())
	if err != nil {
		return
	}
	if _, err := a.client.Do(ctx, "POST", "/v1/channels/"+a.cfg.Channel+"/inbox/phone", protocol.InboxPost{Box: box, Push: true}, nil); err != nil {
		a.log.Warn("notify", "type", typ, "err", err)
	}
}

func (a *Agent) reply(ctx context.Context, ref, typ string, body any) {
	b, _ := json.Marshal(body)
	box, err := protocol.SealMessage(a.key, protocol.DeckMessage{ID: protocol.RandomID(16), Type: typ, Ref: ref, Body: b}, time.Now())
	if err != nil {
		return
	}
	if _, err := a.client.Do(ctx, "POST", "/v1/channels/"+a.cfg.Channel+"/inbox/phone", protocol.InboxPost{Box: box}, nil); err != nil {
		a.log.Warn("reply", "err", err)
	}
}

type authedKey struct{}

func isAuthed(ctx context.Context) bool {
	v, _ := ctx.Value(authedKey{}).(bool)
	return v
}

var (
	errNeedsFinger  = errors.New("needs your fingerprint")
	errNeedsSession = errors.New("open remote control with your fingerprint first")
)

func (a *Agent) dispatch(ctx context.Context, m protocol.DeckMessage) (json.RawMessage, error) {
	authed := protocol.VerifyDeckAuth(a.phonePub, m)
	if m.Type == "run" {
		var ch Chain
		if err := json.Unmarshal(m.Body, &ch); err != nil {
			return nil, err
		}
		return nil, a.runChain(ctx, ch, authed)
	}
	act, ok := actions[m.Type]
	if !ok {
		return nil, fmt.Errorf("unknown action %q", m.Type)
	}
	if act.auth && !authed {
		return nil, errNeedsFinger
	}
	if act.session && !authed && !a.sessionActive() {
		return nil, errNeedsSession
	}
	return act.fn(a, context.WithValue(ctx, authedKey{}, authed), m.Body)
}

// Chain is a shortcut: steps run in order, the first failure stops it.
type Chain struct {
	Name  string `json:"name,omitempty"`
	Steps []Step `json:"steps"`
}

type Step struct {
	Type string          `json:"type"`
	Args json.RawMessage `json:"args,omitempty"`
}

func (a *Agent) runChain(ctx context.Context, ch Chain, authed bool) error {
	if len(ch.Steps) == 0 || len(ch.Steps) > 50 {
		return errors.New("a shortcut needs 1–50 steps")
	}
	// check everything first so a chain never stops halfway on a permission problem
	for i, s := range ch.Steps {
		act, ok := actions[s.Type]
		if !ok || s.Type == "state" {
			return fmt.Errorf("step %d: unknown action %q", i+1, s.Type)
		}
		if act.auth && !authed {
			return fmt.Errorf("step %d (%s): %w", i+1, s.Type, errNeedsFinger)
		}
		if act.session && !authed && !a.sessionActive() {
			return fmt.Errorf("step %d (%s): %w", i+1, s.Type, errNeedsSession)
		}
	}
	// keep going: one missing feature (say, no do-not-disturb daemon) should
	// not cancel the rest of the routine; report what failed at the end
	var failed []string
	for i, s := range ch.Steps {
		if _, err := actions[s.Type].fn(a, ctx, s.Args); err != nil {
			failed = append(failed, fmt.Sprintf("step %d (%s): %v", i+1, s.Type, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// SelfTest reads the live state through the same actions the phone uses.
func SelfTest(ctx context.Context, sys System, print func(any)) error {
	hn, _ := os.Hostname()
	a := &Agent{sys: sys, batteryDir: "/sys/class/power_supply", hostname: hn}
	data, err := a.state(ctx, nil)
	if err != nil {
		return err
	}
	var st State
	json.Unmarshal(data, &st)
	print(st)
	apps := installedApps(defaultDataDirs())
	print(map[string]int{"installed_apps": len(apps)})
	return nil
}
