package protocol

// PocketDeck: phone and notebook share an AES-256-GCM key from ECDH (P-256)
// agreed at pairing. Everything through the relay is sealed with it, so the
// relay only moves opaque boxes. Actions that need the user's finger carry an
// extra signature by the phone's biometric key.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

const (
	deckInfo     = "pocketdeck-v1"
	deckAuthPfx  = "pocketdeck-v1-auth\n"
	pairV2Prefix = "pocketsudo-v2-pair\n"
	MaxClockSkew = 5 * time.Minute
	AppSudo      = "sudo"
	AppDeck      = "deck"
)

func GenerateKX() (*ecdh.PrivateKey, error) { return ecdh.P256().GenerateKey(rand.Reader) }

// EncodeKXPublic returns base64 SPKI DER, what Android's PublicKey.getEncoded() gives.
func EncodeKXPublic(pub *ecdh.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func DecodeKXPublic(s string) (*ecdh.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	switch p := k.(type) {
	case *ecdsa.PublicKey:
		return p.ECDH()
	case *ecdh.PublicKey:
		return p, nil
	}
	return nil, errors.New("not an EC key")
}

// DeckKey derives the channel's AES key: HKDF-SHA256(ECDH, salt=channel, info=pocketdeck-v1).
func DeckKey(own *ecdh.PrivateKey, peerB64, channel string) ([]byte, error) {
	peer, err := DecodeKXPublic(peerB64)
	if err != nil {
		return nil, fmt.Errorf("peer key: %w", err)
	}
	shared, err := own.ECDH(peer)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, shared, []byte(channel), deckInfo, 32)
}

// Seal returns base64(nonce || ciphertext+tag).
func Seal(key, plain []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func Open(key []byte, box string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(box)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("box too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// DeckMessage is the plaintext inside every box.
type DeckMessage struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   int64           `json:"ts"`
	Body json.RawMessage `json:"body,omitempty"`
	// Ref points a result at its command.
	Ref string `json:"ref,omitempty"`
	// Sig is the biometric signature for actions that need the user's finger.
	Sig string `json:"sig,omitempty"`
}

func SealMessage(key []byte, m DeckMessage, now time.Time) (string, error) {
	if m.TS == 0 {
		m.TS = now.Unix()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return Seal(key, b)
}

// ReplayGuard remembers message ids for the freshness window.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewReplayGuard() *ReplayGuard { return &ReplayGuard{seen: map[string]time.Time{}} }

func (g *ReplayGuard) check(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, t := range g.seen {
		if now.Sub(t) > 2*MaxClockSkew {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[id]; dup {
		return false
	}
	g.seen[id] = now
	return true
}

func OpenMessage(key []byte, box string, now time.Time, guard *ReplayGuard) (DeckMessage, error) {
	plain, err := Open(key, box)
	if err != nil {
		return DeckMessage{}, fmt.Errorf("open: %w", err)
	}
	var m DeckMessage
	if err := json.Unmarshal(plain, &m); err != nil {
		return DeckMessage{}, err
	}
	ts := time.Unix(m.TS, 0)
	if m.ID == "" || ts.Before(now.Add(-MaxClockSkew)) || ts.After(now.Add(MaxClockSkew)) {
		return DeckMessage{}, errors.New("message id missing or timestamp outside the window")
	}
	if guard != nil && !guard.check(m.ID, now) {
		return DeckMessage{}, errors.New("replayed message")
	}
	return m, nil
}

// DeckAuthMessage is what the phone's biometric key signs.
func DeckAuthMessage(m DeckMessage) []byte {
	sum := sha256.Sum256(m.Body)
	return []byte(deckAuthPfx + m.ID + "\n" + m.Type + "\n" + strconv.FormatInt(m.TS, 10) + "\n" + hex.EncodeToString(sum[:]))
}

func SignDeckAuth(key *ecdsa.PrivateKey, m DeckMessage) (string, error) {
	return sign(key, DeckAuthMessage(m))
}

func VerifyDeckAuth(pub *ecdsa.PublicKey, m DeckMessage) bool {
	return m.Sig != "" && verify(pub, m.Sig, DeckAuthMessage(m))
}

// PairMACv2 also covers the key-exchange key, so the relay cannot swap it.
func PairMACv2(secret []byte, channel, phonePub, phoneKX, deviceName string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(pairV2Prefix + channel + "\n" + phonePub + "\n" + phoneKX + "\n" + deviceName))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func CheckPairMACv2(secret []byte, channel, phonePub, phoneKX, deviceName, mac string) bool {
	return hmac.Equal([]byte(PairMACv2(secret, channel, phonePub, phoneKX, deviceName)), []byte(mac))
}
