package protocol

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sampleRequest() Request {
	return Request{V: Version, ID: RandomID(16), Channel: "c1", Hostname: "eclipse", User: "tom",
		Service: "sudo", TTY: "/dev/pts/3", Command: "pacman -Syu", Code: "42",
		Created: 1000, Expires: 1060, Nonce: RandomID(16)}
}

func TestRequestRoundTrip(t *testing.T) {
	host := mustKey(t)
	sr, err := SignRequest(host, sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyRequest(&host.PublicKey, sr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "pacman -Syu" || got.Code != "42" {
		t.Fatalf("unexpected request %+v", got)
	}
}

func TestRequestTamperingIsRejected(t *testing.T) {
	host := mustKey(t)
	sr, _ := SignRequest(host, sampleRequest())
	payload, _ := base64.StdEncoding.DecodeString(sr.Payload)
	forged := strings.Replace(string(payload), "pacman -Syu", "rm -rf /home", 1)
	sr.Payload = base64.StdEncoding.EncodeToString([]byte(forged))
	if _, err := VerifyRequest(&host.PublicKey, sr); err == nil {
		t.Fatal("tampered request verified")
	}
	other := mustKey(t)
	sr2, _ := SignRequest(other, sampleRequest())
	if _, err := VerifyRequest(&host.PublicKey, sr2); err == nil {
		t.Fatal("request signed by foreign key verified")
	}
}

func TestResponseIsBoundToRequestAndDecision(t *testing.T) {
	host, phone := mustKey(t), mustKey(t)
	sr1, _ := SignRequest(host, sampleRequest())
	sr2, _ := SignRequest(host, sampleRequest())
	resp, err := SignResponse(phone, DecisionApprove, sr1.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyResponse(&phone.PublicKey, sr1.Payload, resp) {
		t.Fatal("valid approval rejected")
	}
	if VerifyResponse(&phone.PublicKey, sr2.Payload, resp) {
		t.Fatal("approval replayed onto another request")
	}
	deny, _ := SignResponse(phone, DecisionDeny, sr1.Payload)
	deny.Decision = DecisionApprove
	if VerifyResponse(&phone.PublicKey, sr1.Payload, deny) {
		t.Fatal("deny turned into approve")
	}
	if VerifyResponse(&host.PublicKey, sr1.Payload, resp) {
		t.Fatal("approval verified with wrong key")
	}
}

func TestPublicKeyEncoding(t *testing.T) {
	k := mustKey(t)
	s, err := EncodePublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodePublicKey(s)
	if err != nil || !back.Equal(&k.PublicKey) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := DecodePublicKey("bm9wZQ=="); err == nil {
		t.Fatal("garbage accepted as key")
	}
}

func TestPairMACAndCode(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	mac := PairMAC(secret, "c1", "PUB", "S24 FE")
	if !CheckPairMAC(secret, "c1", "PUB", "S24 FE", mac) {
		t.Fatal("valid mac rejected")
	}
	if CheckPairMAC(secret, "c1", "OTHERPUB", "S24 FE", mac) {
		t.Fatal("mac accepted for swapped key")
	}
	c := VerificationCode("a", "b")
	if len(c) != 6 || c != VerificationCode("a", "b") || c == VerificationCode("b", "a") {
		t.Fatalf("bad verification code %q", c)
	}
	if code := RandomCode(); len(code) != 2 {
		t.Fatalf("bad random code %q", code)
	}
}

// TestWriteAndroidVectors keeps ../../android/app/src/test/resources/vectors.json
// in sync so the Java side is tested against signatures Go actually produces.
func TestWriteAndroidVectors(t *testing.T) {
	path := "../android/app/src/test/resources/vectors.json"
	if os.Getenv("UPDATE_VECTORS") == "" {
		if _, err := os.Stat(path); err != nil {
			t.Skip("vectors not generated yet; run with UPDATE_VECTORS=1")
		}
		var v map[string]string
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		hostPub, err := DecodePublicKey(v["host_pub"])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyRequest(hostPub, SignedRequest{Payload: v["payload"], Sig: v["sig"]}); err != nil {
			t.Fatal(err)
		}
		phonePub, _ := DecodePublicKey(v["phone_pub"])
		if !VerifyResponse(phonePub, v["payload"], Response{Decision: DecisionApprove, Sig: v["approve_sig"]}) {
			t.Fatal("stored approval does not verify")
		}
		return
	}
	host, phone := mustKey(t), mustKey(t)
	sr, _ := SignRequest(host, sampleRequest())
	resp, _ := SignResponse(phone, DecisionApprove, sr.Payload)
	hostPub, _ := EncodePublicKey(&host.PublicKey)
	phonePub, _ := EncodePublicKey(&phone.PublicKey)
	phonePriv, _ := x509.MarshalPKCS8PrivateKey(phone)
	msg, _ := ResponseMessage(DecisionApprove, sr.Payload)
	fp, _ := KeyFingerprint(hostPub)
	secret := []byte("0123456789abcdef0123456789abcdef")
	out := map[string]string{
		"host_pub": hostPub, "host_fp": fp, "payload": sr.Payload, "sig": sr.Sig,
		"phone_pub": phonePub, "phone_priv_pkcs8": base64.StdEncoding.EncodeToString(phonePriv),
		"approve_sig": resp.Sig, "approve_msg": string(msg),
		"pair_secret": base64.RawURLEncoding.EncodeToString(secret), "pair_channel": "c1", "pair_device": "S24 FE",
		"pair_mac":          PairMAC(secret, "c1", phonePub, "S24 FE"),
		"verification_code": VerificationCode(hostPub, phonePub),
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := os.MkdirAll("../android/app/src/test/resources", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
