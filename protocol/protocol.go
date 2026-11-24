// Package protocol defines the messages exchanged between the notebook (host),
// the relay and the phone. The relay is untrusted: every request is signed by
// the host key and every decision by the phone's Keystore key, so a
// compromised relay can only drop messages, never forge an approval.
package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	Version = 1

	requestPrefix  = "pocketsudo-v1-request\n"
	responsePrefix = "pocketsudo-v1-response\n"
	pairPrefix     = "pocketsudo-v1-pair\n"

	DecisionApprove = "approve"
	DecisionDeny    = "deny"
)

// Request is what the phone shows before the user decides.
type Request struct {
	V        int    `json:"v"`
	ID       string `json:"id"`
	Channel  string `json:"channel"`
	Hostname string `json:"hostname"`
	User     string `json:"user"`
	Service  string `json:"service"`
	TTY      string `json:"tty,omitempty"`
	RHost    string `json:"rhost,omitempty"`
	Remote   bool   `json:"remote"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Code     string `json:"code"`
	Created  int64  `json:"created"`
	Expires  int64  `json:"expires"`
	Nonce    string `json:"nonce"`
}

// SignedRequest travels through the relay. Payload is the exact JSON the host
// signed, kept as bytes so nobody has to re-serialize it identically.
type SignedRequest struct {
	Payload string `json:"payload"` // base64 std of the Request JSON
	Sig     string `json:"sig"`     // base64 std, ASN.1 DER ECDSA P-256/SHA-256
}

type Response struct {
	Decision string `json:"decision"`
	Sig      string `json:"sig"`
}

func RandomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RandomCode is the short number shown on both terminal and phone so the user
// can tell their own request apart from one somebody else triggered.
func RandomCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%02d", binary.BigEndian.Uint32(b[:])%100)
}

func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// EncodePublicKey returns base64 SPKI DER, the format Android's
// PublicKey.getEncoded() produces.
func EncodePublicKey(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func DecodePublicKey(s string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("public key: not an EC P-256 key")
	}
	return ec, nil
}

// KeyFingerprint is base64url(sha256(SPKI DER)), used in the pairing QR code.
func KeyFingerprint(pubB64 string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func sign(key *ecdsa.PrivateKey, parts ...[]byte) (string, error) {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	sig, err := ecdsa.SignASN1(rand.Reader, key, h.Sum(nil))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

func verify(pub *ecdsa.PublicKey, sigB64 string, parts ...[]byte) bool {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return ecdsa.VerifyASN1(pub, h.Sum(nil), sig)
}

func SignRequest(key *ecdsa.PrivateKey, r Request) (SignedRequest, error) {
	payload, err := json.Marshal(r)
	if err != nil {
		return SignedRequest{}, err
	}
	sig, err := sign(key, []byte(requestPrefix), payload)
	if err != nil {
		return SignedRequest{}, err
	}
	return SignedRequest{Payload: base64.StdEncoding.EncodeToString(payload), Sig: sig}, nil
}

// VerifyRequest checks the host signature and returns the decoded request.
func VerifyRequest(hostPub *ecdsa.PublicKey, s SignedRequest) (Request, error) {
	payload, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil {
		return Request{}, fmt.Errorf("payload: %w", err)
	}
	if !verify(hostPub, s.Sig, []byte(requestPrefix), payload) {
		return Request{}, errors.New("bad host signature")
	}
	var r Request
	if err := json.Unmarshal(payload, &r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// ResponseMessage is what the phone signs. Binding it to the payload hash
// (which contains a fresh id and nonce) makes old approvals worthless.
func ResponseMessage(decision string, payloadB64 string) ([]byte, error) {
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return []byte(responsePrefix + decision + "\n" + hex.EncodeToString(sum[:])), nil
}

func SignResponse(key *ecdsa.PrivateKey, decision string, payloadB64 string) (Response, error) {
	msg, err := ResponseMessage(decision, payloadB64)
	if err != nil {
		return Response{}, err
	}
	sig, err := sign(key, msg)
	if err != nil {
		return Response{}, err
	}
	return Response{Decision: decision, Sig: sig}, nil
}

func VerifyResponse(phonePub *ecdsa.PublicKey, payloadB64 string, resp Response) bool {
	if resp.Decision != DecisionApprove && resp.Decision != DecisionDeny {
		return false
	}
	msg, err := ResponseMessage(resp.Decision, payloadB64)
	if err != nil {
		return false
	}
	return verify(phonePub, resp.Sig, msg)
}

// PairMAC proves to the host that the phone key came from someone who saw the
// QR code; the relay never learns the secret.
func PairMAC(secret []byte, channel, phonePub, deviceName string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(pairPrefix + channel + "\n" + phonePub + "\n" + deviceName))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func CheckPairMAC(secret []byte, channel, phonePub, deviceName, mac string) bool {
	want := PairMAC(secret, channel, phonePub, deviceName)
	return hmac.Equal([]byte(want), []byte(mac))
}

// VerificationCode is shown on both devices after pairing (6 digits).
func VerificationCode(hostPub, phonePub string) string {
	sum := sha256.Sum256([]byte(hostPub + "\n" + phonePub))
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1000000)
}

func Now() int64 { return time.Now().Unix() }
