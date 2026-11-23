package host

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/protocol"
)

// Session describes what is being authorized; it ends up on the phone screen.
type Session struct {
	Hostname string
	Service  string
	User     string
	TTY      string
	RHost    string
	Remote   bool
	Command  string
	Cwd      string
}

type AuthUI struct {
	Out  io.Writer
	Skip <-chan struct{} // fires when the user wants the password prompt instead
}

// LocalVerifier runs the built-in fingerprint reader; nil error means a match.
type LocalVerifier func(ctx context.Context, user string) error

type Result struct {
	Approved bool
	Via      string // device name, "fingerprint reader", or empty
	Reason   string
}

type outcome struct {
	approved bool
	final    bool // ends the whole attempt (approval or explicit phone denial)
	via      string
	msg      string
}

const maxCommandLen = 512

func Authenticate(ctx context.Context, cfg *Config, s Session, ui AuthUI, local LocalVerifier) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()
	out := ui.Out
	if out == nil {
		out = io.Discard
	}

	usePhone := len(cfg.Pairings) > 0 && (!cfg.RemoteOnly || s.Remote)
	useLocal := local != nil && cfg.LocalFingerprint != "off" && !s.Remote
	if !usePhone && !useLocal {
		return Result{Reason: "nothing to ask"}
	}

	results := make(chan outcome, len(cfg.Pairings)+2)
	var wg sync.WaitGroup
	var submitted []submittedReq
	code := protocol.RandomCode()

	if usePhone {
		key, err := cfg.Key()
		if err != nil {
			fmt.Fprintln(out, "pocketsudo: host key unreadable:", err)
			usePhone = false
		} else {
			submitted = submitAll(ctx, cfg, key, s, code, out)
			usePhone = len(submitted) > 0
		}
	}
	if !usePhone && !useLocal {
		return Result{Reason: "relay unreachable"}
	}

	var hint []string
	if usePhone {
		names := make([]string, len(submitted))
		for i, r := range submitted {
			names[i] = r.pairing.DeviceName
		}
		hint = append(hint, fmt.Sprintf("confirm on %s (code %s)", strings.Join(names, " / "), code))
	}
	if useLocal {
		hint = append(hint, "touch the fingerprint reader")
	}
	line := "pocketsudo: " + strings.Join(hint, " or ")
	if ui.Skip != nil {
		line += " – Enter for password"
	}
	fmt.Fprintln(out, line)

	for _, r := range submitted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waitPhone(ctx, r, out, results)
		}()
	}
	if useLocal {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := local(ctx, s.User); err == nil {
				results <- outcome{approved: true, final: true, via: "fingerprint reader"}
			} else if ctx.Err() == nil {
				results <- outcome{msg: "fingerprint reader: " + err.Error()}
			}
		}()
	}

	res := Result{Reason: "timed out"}
	open := len(submitted)
	if useLocal {
		open++
	}
loop:
	for open > 0 {
		select {
		case o := <-results:
			if o.final {
				res = Result{Approved: o.approved, Via: o.via, Reason: o.msg}
				break loop
			}
			open--
			if o.msg != "" {
				fmt.Fprintln(out, "pocketsudo:", o.msg)
				res.Reason = o.msg
			}
		case <-ui.Skip:
			res = Result{Reason: "skipped"}
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	cancel()

	// Tell the phones to drop the notification; this must not hang sudo.
	cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ccancel()
	for _, r := range submitted {
		if r.pairing.DeviceName == res.Via && res.Approved {
			continue
		}
		r.client.Do(cctx, "DELETE", "/v1/channels/"+r.pairing.Channel+"/requests/"+r.req.ID, nil, nil)
	}
	wg.Wait()
	switch {
	case res.Approved:
		fmt.Fprintf(out, "pocketsudo: approved via %s\n", res.Via)
	case res.Reason == "skipped":
	case res.Reason != "":
		fmt.Fprintln(out, "pocketsudo:", res.Reason)
	}
	return res
}

type submittedReq struct {
	pairing Pairing
	client  *Client
	req     protocol.Request
	signed  protocol.SignedRequest
}

func submitAll(ctx context.Context, cfg *Config, key *ecdsa.PrivateKey, s Session, code string, out io.Writer) []submittedReq {
	now := time.Now()
	cmd := s.Command
	if len(cmd) > maxCommandLen {
		cmd = cmd[:maxCommandLen] + "…"
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var res []submittedReq
	for _, p := range cfg.Pairings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := protocol.Request{V: protocol.Version, ID: protocol.RandomID(16), Channel: p.Channel,
				Hostname: s.Hostname, User: s.User, Service: s.Service, TTY: s.TTY, RHost: s.RHost,
				Remote: s.Remote, Command: cmd, Cwd: s.Cwd, Code: code, Created: now.Unix(),
				Expires: now.Add(time.Duration(cfg.TimeoutSec) * time.Second).Unix(), Nonce: protocol.RandomID(16)}
			sr, err := protocol.SignRequest(key, r)
			if err != nil {
				return
			}
			c := NewClient(p.Relay, p.HostToken)
			sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			var sub protocol.SubmitResp
			if _, err := c.Do(sctx, "POST", "/v1/channels/"+p.Channel+"/requests", sr, &sub); err != nil {
				fmt.Fprintf(out, "pocketsudo: %s unreachable (%v)\n", p.DeviceName, shortErr(err))
				return
			}
			if sub.Push == "failed" {
				fmt.Fprintf(out, "pocketsudo: push to %s failed – open the app to see the request\n", p.DeviceName)
			}
			mu.Lock()
			res = append(res, submittedReq{pairing: p, client: c, req: r, signed: sr})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return res
}

func shortErr(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

func waitPhone(ctx context.Context, r submittedReq, out io.Writer, results chan<- outcome) {
	phonePub, err := protocol.DecodePublicKey(r.pairing.PhonePub)
	if err != nil {
		results <- outcome{msg: r.pairing.DeviceName + ": stored key broken"}
		return
	}
	known := protocol.ReqPending
	path := "/v1/channels/" + r.pairing.Channel + "/requests/" + r.req.ID + "/response?wait=25&known="
	for ctx.Err() == nil {
		var st protocol.RequestState
		if _, err := r.client.Do(ctx, "GET", path+known, nil, &st); err != nil {
			sleep(ctx, time.Second)
			continue
		}
		switch st.State {
		case protocol.ReqPending:
		case protocol.ReqSeen:
			if known != protocol.ReqSeen {
				fmt.Fprintf(out, "pocketsudo: opened on %s\n", r.pairing.DeviceName)
			}
			known = protocol.ReqSeen
		case protocol.ReqAnswered:
			switch {
			case st.Response != nil && st.Response.Decision == protocol.DecisionDeny:
				results <- outcome{final: true, msg: "denied on " + r.pairing.DeviceName}
			case st.Response != nil && protocol.VerifyResponse(phonePub, r.signed.Payload, *st.Response):
				results <- outcome{approved: true, final: true, via: r.pairing.DeviceName}
			default:
				results <- outcome{msg: r.pairing.DeviceName + ": answer with invalid signature ignored"}
			}
			return
		default:
			if ctx.Err() == nil {
				results <- outcome{msg: r.pairing.DeviceName + ": request " + st.State}
			}
			return
		}
	}
}

// IsTerminal reports whether f looks like a character device we can read Enter from.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
