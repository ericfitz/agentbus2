// Package keys generates, encodes and identifies the Ed25519 and ES256 key
// pairs of Decision 55. It imports only the standard library and go-jose.
package keys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// Algorithm names a JOSE signature algorithm the system admits.
type Algorithm string

// The two algorithms of Decision 55; they are the values store.PublicKey.Algorithm admits.
const (
	EdDSA Algorithm = "EdDSA"
	ES256 Algorithm = "ES256"
)

const pemPrivateType = "PRIVATE KEY"

// Generate makes a new key pair of the given algorithm.
func Generate(alg Algorithm) (crypto.Signer, error) {
	switch alg {
	case EdDSA:
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("keys: generate ed25519: %w", err)
		}
		return priv, nil
	case ES256:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("keys: generate p-256: %w", err)
		}
		return priv, nil
	default:
		return nil, fmt.Errorf("keys: unsupported algorithm %q", string(alg))
	}
}

// AlgorithmOf reports the algorithm of a public key: ed25519 is EdDSA and
// ECDSA on P-256 is ES256. Anything else, a private key included, is an error.
func AlgorithmOf(pub crypto.PublicKey) (Algorithm, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if len(k) != ed25519.PublicKeySize {
			return "", errors.New("keys: unsupported key type: malformed ed25519 public key")
		}
		return EdDSA, nil
	case *ecdsa.PublicKey:
		if k != nil && k.Curve == elliptic.P256() {
			return ES256, nil
		}
		return "", errors.New("keys: unsupported key type: ecdsa curve is not P-256")
	default:
		return "", fmt.Errorf("keys: unsupported key type %T", pub)
	}
}

// KID is the RFC 7638 SHA-256 thumbprint of the public key, base64url without padding.
func KID(pub crypto.PublicKey) (string, error) {
	if _, err := AlgorithmOf(pub); err != nil {
		return "", err
	}
	j := jose.JSONWebKey{Key: pub}
	sum, err := j.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("keys: thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// EncodePrivatePEM encodes the key as one PKCS#8 "PRIVATE KEY" block (Decision 55).
func EncodePrivatePEM(k crypto.Signer) ([]byte, error) {
	if k == nil {
		return nil, errors.New("keys: nil key")
	}
	if _, err := AlgorithmOf(k.Public()); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, fmt.Errorf("keys: marshal pkcs8: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPrivateType, Bytes: der}), nil
}

// ParsePrivatePEM parses exactly one PEM block of type "PRIVATE KEY". Any other
// block type, a second block or trailing non-PEM bytes is an error.
func ParsePrivatePEM(b []byte) (crypto.Signer, Algorithm, error) {
	block, rest := pem.Decode(b)
	if block == nil {
		return nil, "", errors.New("keys: no PEM block found")
	}
	if block.Type != pemPrivateType {
		return nil, "", fmt.Errorf("keys: PEM block type %q, want %q", block.Type, pemPrivateType)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, "", errors.New("keys: trailing bytes or a second PEM block after the private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("keys: parse pkcs8: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, "", fmt.Errorf("keys: unsupported key type %T", parsed)
	}
	alg, err := AlgorithmOf(signer.Public())
	if err != nil {
		return nil, "", err
	}
	return signer, alg, nil
}

// MarshalPublic is the PKIX DER form, the store.PublicKey.Key form.
func MarshalPublic(pub crypto.PublicKey) ([]byte, error) {
	if _, err := AlgorithmOf(pub); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("keys: marshal pkix: %w", err)
	}
	return der, nil
}

// ParsePublic parses PKIX DER and reports its algorithm.
func ParsePublic(der []byte) (crypto.PublicKey, Algorithm, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", fmt.Errorf("keys: parse pkix: %w", err)
	}
	alg, err := AlgorithmOf(pub)
	if err != nil {
		return nil, "", err
	}
	return pub, alg, nil
}

// ToJWK wraps a public key as a JWK with alg, use "sig" and the given kid.
func ToJWK(pub crypto.PublicKey, kid string) (jose.JSONWebKey, error) {
	alg, err := AlgorithmOf(pub)
	if err != nil {
		return jose.JSONWebKey{}, err
	}
	return jose.JSONWebKey{Key: pub, KeyID: kid, Algorithm: string(alg), Use: "sig"}, nil
}

// FromJWK extracts a public key from a JWK. A JWK with no key, private material, an unsupported
// key type, a kid that is not the key's thumbprint and an alg naming the other
// algorithm are errors.
func FromJWK(j jose.JSONWebKey) (crypto.PublicKey, Algorithm, string, error) {
	if j.Key == nil {
		return nil, "", "", errors.New("keys: JWK has no key")
	}
	if !j.IsPublic() {
		return nil, "", "", errors.New("keys: JWK carries private key material")
	}
	alg, err := AlgorithmOf(j.Key)
	if err != nil {
		return nil, "", "", err
	}
	kid, err := KID(j.Key)
	if err != nil {
		return nil, "", "", err
	}
	if j.KeyID != "" && j.KeyID != kid {
		return nil, "", "", errors.New("keys: kid does not match the key")
	}
	if j.Algorithm != "" && j.Algorithm != string(alg) {
		return nil, "", "", fmt.Errorf("keys: JWK alg %q does not match the %s key", j.Algorithm, alg)
	}
	return j.Key, alg, kid, nil
}
