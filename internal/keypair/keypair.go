// Package keypair generates the RSA-2048 keypair used for Arc enrollment.
//
// The wire format azcmagent expects on the VM side is PKCS#1 DER, base64
// encoded — both for the private key passed to `azcmagent connect existing
// --private-key` and for the public key written to the Arc machine resource's
// `properties.publicKey` field.
//
// Keys live entirely in process memory: the public half is uploaded to the
// Arc machine resource at create time, the private half is handed to the
// in-VM script via the action-style runCommand API's `parameters` field
// (TLS-encrypted in transit, never persisted in ARM because the action-style
// API creates no tracked resource). Nothing is ever written to disk by
// arcify.
package keypair

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
)

// Keypair carries the generated RSA key in the encodings arcify needs.
type Keypair struct {
	PrivateKey *rsa.PrivateKey

	// PrivateKeyDERBase64 is base64(PKCS#1 DER) — what azcmagent's
	// --private-key flag expects.
	PrivateKeyDERBase64 string

	// PublicKeyDERBase64 is base64(PKCS#1 DER) — what we PUT to
	// Microsoft.HybridCompute/machines properties.publicKey.
	PublicKeyDERBase64 string

	// VMID is a freshly generated UUID stamped on the Arc resource at
	// precreate time and passed back in via --vmid on the agent side; the
	// agent uses it to bind itself to the precreated record.
	VMID string
}

// Generate produces a fresh RSA-2048 keypair plus a VMID.
func Generate() (*Keypair, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("rsa.GenerateKey: %w", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	pubDER := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
	return &Keypair{
		PrivateKey:          priv,
		PrivateKeyDERBase64: base64.StdEncoding.EncodeToString(privDER),
		PublicKeyDERBase64:  base64.StdEncoding.EncodeToString(pubDER),
		VMID:                uuid.NewString(),
	}, nil
}
