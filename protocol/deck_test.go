package protocol

import (
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func mustKX(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := GenerateKX()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKXEncodingAndAgreement(t *testing.T) {
	host, phone := mustKX(t), mustKX(t)
	hostPub, err := EncodeKXPublic(host.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	phonePub, _ := EncodeKXPublic(phone.PublicKey())
	k1, err := DeckKey(host, phonePub, "chan")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeckKey(phone, hostPub, "chan")
	if err != nil {
		t.Fatal(err)
	}
	if string(k1) != string(k2) || len(k1) != 32 {
		t.Fatal("both sides must derive the same 32-byte key")
	}
	k3, _ := DeckKey(phone, hostPub, "other-chan")
	if string(k3) == string(k1) {
		t.Fatal("key must be bound to the channel")
	}
	if _, err := DeckKey(host, "bm9wZQ==", "chan"); err == nil {
		t.Fatal("garbage public key accepted")
	}
}

func TestSealOpen(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := Seal(key, []byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, box)
	if err != nil || string(plain) != `{"hello":"world"}` {
		t.Fatalf("open: %v %q", err, plain)
	}
	raw, _ := base64.StdEncoding.DecodeString(box)
	raw[len(raw)-1] ^= 1
	if _, err := Open(key, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered box opened")
	}
	other := make([]byte, 32)
	if _, err := Open(other, box); err == nil {
		t.Fatal("opened with the wrong key")
	}
	box2, _ := Seal(key, []byte(`{"hello":"world"}`))
	if box2 == box {
		t.Fatal("nonce reused")
	}
}

func TestDeckMessageRoundTripAndFreshness(t *testing.T) {
	key := make([]byte, 32)
	now := time.Unix(1_800_000_000, 0)
	m := DeckMessage{ID: RandomID(16), Type: "volume.set", Body: json.RawMessage(`{"percent":40}`)}
	box, err := SealMessage(key, m, now)
	if err != nil {
		t.Fatal(err)
	}
	seen := NewReplayGuard()
	got, err := OpenMessage(key, box, now.Add(2*time.Second), seen)
	if err != nil || got.Type != "volume.set" || string(got.Body) != `{"percent":40}` {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := OpenMessage(key, box, now.Add(3*time.Second), seen); err == nil {
		t.Fatal("replayed message accepted")
	}
	old, _ := SealMessage(key, DeckMessage{ID: RandomID(16), Type: "x"}, now.Add(-10*time.Minute))
	if _, err := OpenMessage(key, old, now, NewReplayGuard()); err == nil {
		t.Fatal("stale message accepted")
	}
}

func TestDeckAuthSignature(t *testing.T) {
	phone, _ := GenerateKey()
	other, _ := GenerateKey()
	m := DeckMessage{ID: RandomID(16), Type: "unlock", TS: 1_800_000_000, Body: json.RawMessage(`{}`)}
	sig, err := SignDeckAuth(phone, m)
	if err != nil {
		t.Fatal(err)
	}
	m.Sig = sig
	if !VerifyDeckAuth(&phone.PublicKey, m) {
		t.Fatal("valid auth rejected")
	}
	if VerifyDeckAuth(&other.PublicKey, m) {
		t.Fatal("foreign key accepted")
	}
	m.Type = "run"
	if VerifyDeckAuth(&phone.PublicKey, m) {
		t.Fatal("signature not bound to the type")
	}
}

func TestPairMACv2CoversKX(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	mac := PairMACv2(secret, "c", "PUB", "KX", "dev")
	if !CheckPairMACv2(secret, "c", "PUB", "KX", "dev", mac) {
		t.Fatal("valid mac rejected")
	}
	if CheckPairMACv2(secret, "c", "PUB", "EVILKX", "dev", mac) {
		t.Fatal("swapped key-exchange key accepted")
	}
}

// TestWriteDeckVectors keeps android/core/src/test/resources/deck_vectors.json in
// sync so the Java side is checked against what Go produces.
func TestWriteDeckVectors(t *testing.T) {
	path := "../android/core/src/test/resources/deck_vectors.json"
	if os.Getenv("UPDATE_VECTORS") == "" {
		t.Skip("run with UPDATE_VECTORS=1 to regenerate")
	}
	phoneKX, _ := GenerateKX()
	hostKX, _ := GenerateKX()
	phonePKCS8, _ := x509.MarshalPKCS8PrivateKey(phoneKX)
	hostPub, _ := EncodeKXPublic(hostKX.PublicKey())
	key, _ := DeckKey(hostKX, mustEnc(EncodeKXPublic(phoneKX.PublicKey())), "chan-1")
	m := DeckMessage{ID: "abc123", Type: "volume.set", TS: 1800000000, Body: json.RawMessage(`{"percent":40}`)}
	box, _ := SealMessage(key, m, time.Unix(1800000000, 0))
	signer, _ := GenerateKey()
	signerPub, _ := EncodePublicKey(&signer.PublicKey)
	m.Sig, _ = SignDeckAuth(signer, m)
	out := map[string]string{
		"phone_kx_pkcs8": base64.StdEncoding.EncodeToString(phonePKCS8), "host_kx_pub": hostPub,
		"channel": "chan-1", "key_b64": base64.StdEncoding.EncodeToString(key), "box": box,
		"auth_msg": string(DeckAuthMessage(m)), "auth_sig": m.Sig, "auth_pub": signerPub,
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustEnc(s string, _ error) string { return s }
