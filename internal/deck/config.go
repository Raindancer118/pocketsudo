// Package deck is the notebook side of PocketDeck: a user service that runs
// commands sealed by the paired phone (unlock, volume, media, shortcuts …).
package deck

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/host"
	"github.com/Raindancer118/pocketsudo/protocol"
)

type Config struct {
	Relay      string    `json:"relay"`
	Channel    string    `json:"channel"`
	HostToken  string    `json:"host_token"`
	HostKey    string    `json:"host_key"` // PKCS#8, ECDSA: shown fingerprint during pairing
	HostKX     string    `json:"host_kx"`  // PKCS#8, ECDH
	HostKXPub  string    `json:"host_kx_pub"`
	PhonePub   string    `json:"phone_pub"` // biometric signing key
	PhoneKX    string    `json:"phone_kx"`
	DeviceName string    `json:"device_name"`
	Paired     time.Time `json:"paired"`
	After      int64     `json:"after"` // inbox cursor

	path string
}

func DefaultPath() string {
	if p := os.Getenv("POCKETDECK_CONFIG"); p != "" {
		return p
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "pocketdeck", "config.json")
}

func Load(path string) (*Config, error) {
	c := &Config{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) IsPaired() bool { return c.Channel != "" && c.PhoneKX != "" }

func encodePKCS8(k any) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func decodePKCS8(s string) (any, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return x509.ParsePKCS8PrivateKey(der)
}

func (c *Config) kxKey() (*ecdh.PrivateKey, error) {
	k, err := decodePKCS8(c.HostKX)
	if err != nil {
		return nil, err
	}
	switch v := k.(type) {
	case *ecdh.PrivateKey:
		return v, nil
	case *ecdsa.PrivateKey:
		return v.ECDH()
	}
	return nil, errors.New("host kx key has the wrong type")
}

// Pair runs the QR handshake for a deck channel and stores the result.
func Pair(ctx context.Context, c *Config, relay, hostname string, ui host.PairUI) error {
	sign, err := protocol.GenerateKey()
	if err != nil {
		return err
	}
	kx, err := protocol.GenerateKX()
	if err != nil {
		return err
	}
	signPub, _ := protocol.EncodePublicKey(&sign.PublicKey)
	kxPub, err := protocol.EncodeKXPublic(kx.PublicKey())
	if err != nil {
		return err
	}
	created, info, err := host.PairChannel(ctx, relay, protocol.CreateChannelReq{
		HostPub: signPub, Hostname: hostname, App: protocol.AppDeck, HostKX: kxPub}, ui,
		func(secret []byte, channel string, info protocol.PairingInfo) bool {
			return info.PhoneKX != "" && protocol.CheckPairMACv2(secret, channel, info.PhonePub, info.PhoneKX, info.DeviceName, info.MAC)
		})
	if err != nil {
		return err
	}
	c.Relay, c.Channel, c.HostToken = relay, created.Channel, created.HostToken
	if c.HostKey, err = encodePKCS8(sign); err != nil {
		return err
	}
	if c.HostKX, err = encodePKCS8(kx); err != nil {
		return err
	}
	c.HostKXPub, c.PhonePub, c.PhoneKX, c.DeviceName = kxPub, info.PhonePub, info.PhoneKX, info.DeviceName
	c.Paired, c.After = time.Now(), 0
	return c.Save()
}
