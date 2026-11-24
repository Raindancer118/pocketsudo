package host

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/Raindancer118/pocketsudo/protocol"
)

// PairURI is what the QR code holds. The secret never reaches the relay.
func PairURI(relay, channel string, secret []byte, hostFP, hostname string) string {
	q := url.Values{}
	q.Set("r", relay)
	q.Set("c", channel)
	q.Set("s", base64.RawURLEncoding.EncodeToString(secret))
	q.Set("f", hostFP)
	q.Set("n", hostname)
	return "pocketsudo://pair?" + q.Encode()
}

type PairUI struct {
	Out io.Writer
	// ShowQR renders the URI; defaults to a terminal QR code.
	ShowQR func(uri string)
	// Confirm asks whether the phone shows the same code.
	Confirm func(device, code string) bool
}

func Pair(ctx context.Context, cfg *Config, relay, hostname string, ui PairUI) (*Pairing, error) {
	key, err := cfg.Key()
	if err != nil {
		return nil, err
	}
	hostPub, err := protocol.EncodePublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	anon := NewClient(relay, "")
	var created protocol.CreateChannelResp
	if _, err := anon.Do(ctx, "POST", "/v1/channels", protocol.CreateChannelReq{HostPub: hostPub, Hostname: hostname}, &created); err != nil {
		return nil, fmt.Errorf("create channel on %s: %w", relay, err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	fp, _ := protocol.KeyFingerprint(hostPub)
	uri := PairURI(relay, created.Channel, secret, fp, hostname)

	if ui.ShowQR == nil {
		ui.ShowQR = func(uri string) { RenderQR(ui.Out, uri) }
	}
	fmt.Fprintln(ui.Out, "Scan this with the phone camera or in the PocketSudo app (Scan code):")
	ui.ShowQR(uri)
	fmt.Fprintln(ui.Out, "Waiting for the phone (10 minutes)…")

	c := NewClient(relay, created.HostToken)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		var info protocol.PairingInfo
		code, err := c.Do(ctx, "GET", "/v1/channels/"+created.Channel+"/pairing?wait=25", nil, &info)
		if err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("pairing timed out")
			}
			fmt.Fprintln(ui.Out, "relay:", err, "– retrying")
			sleep(ctx, 3*time.Second)
			continue
		}
		if code == 204 {
			continue
		}
		reject := func(why string) {
			fmt.Fprintln(ui.Out, why)
			c.Do(ctx, "POST", "/v1/channels/"+created.Channel+"/confirm", protocol.ConfirmReq{Accept: false}, nil)
		}
		if !protocol.CheckPairMAC(secret, created.Channel, info.PhonePub, info.DeviceName, info.MAC) {
			reject("A device without the QR secret tried to pair – rejected, still waiting.")
			continue
		}
		vcode := protocol.VerificationCode(hostPub, info.PhonePub)
		if !ui.Confirm(info.DeviceName, vcode) {
			reject("Pairing rejected.")
			return nil, errors.New("pairing rejected by user")
		}
		if _, err := c.Do(ctx, "POST", "/v1/channels/"+created.Channel+"/confirm", protocol.ConfirmReq{Accept: true}, nil); err != nil {
			return nil, err
		}
		p := Pairing{Relay: relay, Channel: created.Channel, HostToken: created.HostToken,
			PhonePub: info.PhonePub, DeviceName: info.DeviceName, Paired: time.Now()}
		cfg.Pairings = append(cfg.Pairings, p)
		return &p, cfg.Save()
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// Unpair removes a pairing locally and on the relay (best effort).
func Unpair(ctx context.Context, cfg *Config, p Pairing) error {
	NewClient(p.Relay, p.HostToken).Do(ctx, "DELETE", "/v1/channels/"+p.Channel, nil, nil)
	out := cfg.Pairings[:0]
	for _, q := range cfg.Pairings {
		if q.Channel != p.Channel {
			out = append(out, q)
		}
	}
	cfg.Pairings = out
	return cfg.Save()
}

// RenderQR prints uri as a QR code made of half blocks.
func RenderQR(w io.Writer, uri string) {
	qrterminal.GenerateWithConfig(uri, qrterminal.Config{Level: qrterminal.L, Writer: w,
		HalfBlocks: true, BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 2})
}
