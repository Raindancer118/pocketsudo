package deck

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/host"
	"github.com/Raindancer118/pocketsudo/protocol"
)

// System runs desktop commands; tests swap in a recorder.
type System interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	Start(ctx context.Context, name string, args ...string) error // detached, no output
}

// Result is the body of every answer to the phone.
type Result struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type Agent struct {
	cfg        *Config
	sys        System
	log        *slog.Logger
	key        []byte
	phonePub   *ecdsa.PublicKey
	client     *host.Client
	guard      *protocol.ReplayGuard
	batteryDir string
	hostname   string
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
		batteryDir: "/sys/class/power_supply", hostname: hn}, nil
}

// Run long-polls the host inbox until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("pocketdeck agent running", "relay", a.cfg.Relay, "phone", a.cfg.DeviceName)
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

var errNeedsFinger = errors.New("needs your fingerprint")

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
	return act.fn(a, ctx, m.Body)
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
	}
	for i, s := range ch.Steps {
		if _, err := actions[s.Type].fn(a, ctx, s.Args); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, s.Type, err)
		}
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
	apps := installedApps()
	print(map[string]int{"installed_apps": len(apps)})
	return nil
}
