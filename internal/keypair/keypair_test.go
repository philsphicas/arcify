package keypair

import (
	"crypto/x509"
	"encoding/base64"
	"testing"
)

func TestGenerate_Roundtrip(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if kp.PrivateKey == nil {
		t.Fatal("PrivateKey nil")
	}
	if got := kp.PrivateKey.N.BitLen(); got < 2040 || got > 2048 {
		t.Errorf("RSA modulus bit length = %d, want ~2048", got)
	}

	// Private key roundtrip: base64 → PKCS#1 DER → parse.
	privDER, err := base64.StdEncoding.DecodeString(kp.PrivateKeyDERBase64)
	if err != nil {
		t.Fatalf("base64 decode private: %v", err)
	}
	parsedPriv, err := x509.ParsePKCS1PrivateKey(privDER)
	if err != nil {
		t.Fatalf("ParsePKCS1PrivateKey: %v", err)
	}
	if parsedPriv.N.Cmp(kp.PrivateKey.N) != 0 {
		t.Error("private key roundtrip modulus mismatch")
	}

	// Public key roundtrip.
	pubDER, err := base64.StdEncoding.DecodeString(kp.PublicKeyDERBase64)
	if err != nil {
		t.Fatalf("base64 decode public: %v", err)
	}
	parsedPub, err := x509.ParsePKCS1PublicKey(pubDER)
	if err != nil {
		t.Fatalf("ParsePKCS1PublicKey: %v", err)
	}
	if parsedPub.N.Cmp(kp.PrivateKey.N) != 0 {
		t.Error("public key roundtrip modulus mismatch")
	}

	if kp.VMID == "" {
		t.Error("VMID empty")
	}
	if len(kp.VMID) != 36 {
		t.Errorf("VMID len = %d, want 36 (uuid)", len(kp.VMID))
	}
}

func TestGenerate_UniqueVMID(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if a.VMID == b.VMID {
		t.Error("two consecutive Generate() calls returned same VMID")
	}
}
