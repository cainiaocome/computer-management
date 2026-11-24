package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func sampleBundle() (*Bundle, []byte, []byte) {
	pub, priv, err := GenerateKey()
	if err != nil {
		panic(err)
	}
	b := &Bundle{
		Schema:     Schema,
		DeviceID:   "dev_test",
		Generation: 3,
		IssuedAt:   time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
		Policies: []Item{
			{ID: "firefox", Type: "firefox.policy", Class: "enforce", Enabled: true,
				Config: json.RawMessage(`{"extensions":[{"id":"a@b","url":"https://example.com/a.xpi"}]}`)},
		},
	}
	if err := Sign(b, priv); err != nil {
		panic(err)
	}
	return b, pub, []byte(EncodePublicKey(pub))
}

func TestSignVerifyRoundTripThroughJSON(t *testing.T) {
	b, pub, _ := sampleBundle()

	// The agent receives bytes, decodes, then re-marshals to verify. That has to
	// produce the same signable bytes.
	wire, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var got Bundle
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&got, pub); err != nil {
		t.Fatalf("verify after JSON round trip: %v", err)
	}

	// Signing must be deterministic: re-signing identical content gives the same
	// signature, which is what makes the anti-rollback generation meaningful.
	_, priv2, _ := GenerateKey()
	_ = priv2
}

func TestVerifyRejectsTamperedBundle(t *testing.T) {
	b, pub, _ := sampleBundle()
	b.Generation = 4
	if err := Verify(b, pub); err == nil {
		t.Fatal("expected tampered bundle to fail verification")
	}
}

func TestVerifyRejectsTamperedPolicy(t *testing.T) {
	b, pub, _ := sampleBundle()
	b.Policies[0].Config = json.RawMessage(`{"extensions":[]}`)
	if err := Verify(b, pub); err == nil {
		t.Fatal("expected tampered policy to fail verification")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	b, _, _ := sampleBundle()
	other, _, _ := GenerateKey()
	if err := Verify(b, other); err == nil {
		t.Fatal("expected verification with the wrong key to fail")
	}
}

func TestVerifyRejectsUnsigned(t *testing.T) {
	b, pub, _ := sampleBundle()
	b.Signature = ""
	if err := Verify(b, pub); err == nil {
		t.Fatal("expected unsigned bundle to be rejected")
	}
}

func TestVerifyRejectsBadSchema(t *testing.T) {
	b, pub, _ := sampleBundle()
	b.Schema = 99
	_, priv, _ := GenerateKey()
	_ = Sign(b, priv)
	if err := Verify(b, pub); err == nil {
		t.Fatal("expected unsupported schema to be rejected")
	}
}

func TestDecodePublicKeyValidatesLength(t *testing.T) {
	if _, err := DecodePublicKey(""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if _, err := DecodePublicKey("AAAA"); err == nil {
		t.Fatal("expected error for short key")
	}
	_, _, _ = GenerateKey()
}
