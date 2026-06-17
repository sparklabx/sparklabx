// Package connectorauth holds the app's RSA signing key for connector tokens.
//
// The app is the token issuer for data connectors: whatever way a user logged in
// (Google, Microsoft, OIDC, password), the app mints a short-lived RS256 JWT
// describing them, and connectors (Trino, …) validate it against the app's JWKS.
// This decouples login method from connector auth. See docs/connectors-design.md.
package connectorauth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Keys signs connector tokens and exposes the matching public key as a JWKS.
type Keys struct {
	priv   *rsa.PrivateKey
	kid    string
	issuer string
}

// New loads an RSA private key from a PEM string, or generates an ephemeral one
// when pemKey is empty (fine for dev — connectors refetch the JWKS on a new kid;
// set CONNECTOR_JWT_PRIVATE_KEY to a stable key in production). issuer is the
// app's public base URL, used as the token `iss` and checked by connectors.
func New(pemKey, issuer string) (*Keys, error) {
	var priv *rsa.PrivateKey
	if pemKey != "" {
		p, err := parsePEMPrivateKey(pemKey)
		if err != nil {
			return nil, err
		}
		priv = p
	} else {
		p, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate connector signing key: %w", err)
		}
		priv = p
	}
	return &Keys{priv: priv, kid: thumbprint(&priv.PublicKey), issuer: issuer}, nil
}

// LoadOrCreatePEM returns a PEM-encoded RSA private key read from path. If the
// file is missing or empty, it generates a new 2048-bit key, writes it there
// (0600), and returns that — so the signing key, and thus the JWKS `kid`, stays
// stable across restarts without committing a secret or pasting multiline PEM
// into env. An empty path returns ("", nil) so the caller falls back to an
// ephemeral key. Pair with CONNECTOR_JWT_PRIVATE_KEY_FILE + a persistent volume.
func LoadOrCreatePEM(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(b), nil
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("generate connector signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("marshal connector signing key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create connector key dir: %w", err)
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return "", fmt.Errorf("write connector signing key: %w", err)
	}
	return string(pemBytes), nil
}

// GeneratePEM returns a fresh PKCS#8 PEM-encoded 2048-bit RSA private key.
func GeneratePEM() (string, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("generate connector signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("marshal connector signing key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func parsePEMPrivateKey(s string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, fmt.Errorf("connector signing key: invalid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("connector signing key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("connector signing key: not an RSA key")
	}
	return rk, nil
}

// thumbprint is a stable key id derived from the public key.
func thumbprint(pub *rsa.PublicKey) string {
	sum := sha256.Sum256(append(pub.N.Bytes(), byte(pub.E)))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

// Mint signs an RS256 JWT for the given subject identity, valid for ttl.
func (k *Keys) Mint(subject, preferredUsername, email string, ttl time.Duration) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":                k.issuer,
		"sub":                subject,
		"preferred_username": preferredUsername,
		"email":              email,
		"iat":                now.Unix(),
		"exp":                now.Add(ttl).Unix(),
	})
	tok.Header["kid"] = k.kid
	return tok.SignedString(k.priv)
}

// JWKS returns the public key as a JWK Set (what connectors fetch to validate
// app-minted tokens).
func (k *Keys) JWKS() map[string]any {
	pub := &k.priv.PublicKey
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	return map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": k.kid,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(eBytes),
		}},
	}
}
