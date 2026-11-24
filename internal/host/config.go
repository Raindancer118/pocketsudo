// Package host is the notebook side: pairing, the PAM auth helper and the
// PAM file editing.
package host

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Raindancer118/pocketsudo/protocol"
)

const DefaultRelay = "https://pocketsudo.raindancer118.de"

type Pairing struct {
	Relay      string    `json:"relay"`
	Channel    string    `json:"channel"`
	HostToken  string    `json:"host_token"`
	PhonePub   string    `json:"phone_pub"`
	DeviceName string    `json:"device_name"`
	Paired     time.Time `json:"paired"`
}

type Config struct {
	HostKey  string    `json:"host_key"` // base64 PKCS#8
	Pairings []Pairing `json:"pairings"`
	// TimeoutSec is how long sudo waits for phone or finger before the password prompt.
	TimeoutSec int `json:"timeout_sec"`
	// LocalFingerprint races the built-in reader against the phone ("auto" or "off").
	LocalFingerprint string `json:"local_fingerprint"`
	// RemoteOnly asks the phone only for sessions that came in over SSH.
	RemoteOnly bool `json:"remote_only"`
}

func ConfigPath() string {
	if p := os.Getenv("POCKETSUDO_CONFIG"); p != "" {
		return p
	}
	return "/etc/pocketsudo/config.json"
}

func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Config{TimeoutSec: 60, LocalFingerprint: "auto"}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigPath(), err)
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 60
	}
	if c.LocalFingerprint == "" {
		c.LocalFingerprint = "auto"
	}
	return &c, nil
}

func (c *Config) Save() error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Key returns the host signing key, creating it on first use.
func (c *Config) Key() (*ecdsa.PrivateKey, error) {
	if c.HostKey == "" {
		k, err := protocol.GenerateKey()
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		c.HostKey = base64.StdEncoding.EncodeToString(der)
		return k, nil
	}
	der, err := base64.StdEncoding.DecodeString(c.HostKey)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("host key is not ECDSA")
	}
	return ec, nil
}
