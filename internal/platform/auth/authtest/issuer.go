// Package authtest mints RSA signed credentials against an in-process issuer.
// It exists so unit tests and the executable BDD suite exercise the real
// verifier with real signatures instead of a stubbed decision. It must never be
// imported by a runtime entrypoint.
package authtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

const keyBits = 2048

var algorithmHashes = map[string]crypto.Hash{
	"RS256": crypto.SHA256,
	"RS384": crypto.SHA384,
	"RS512": crypto.SHA512,
}

// Issuer is a throwaway identity provider holding one signing key plus a second
// key that is never published, used to forge invalid signatures.
type Issuer struct {
	URL         string
	KeyID       string
	signing     *rsa.PrivateKey
	unpublished *rsa.PrivateKey
}

// NewIssuer generates the key material for one test issuer.
func NewIssuer(url string) (*Issuer, error) {
	signing, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	unpublished, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	return &Issuer{URL: url, KeyID: "test-signing-key", signing: signing, unpublished: unpublished}, nil
}

// Keys returns the published key set, as a service would learn it from JWKS.
func (i *Issuer) Keys() auth.StaticKeys {
	return auth.StaticKeys{i.KeyID: &i.signing.PublicKey}
}

// JWKSHandler serves the published key set so the real JWKS cache can be
// exercised over HTTP.
func (i *Issuer) JWKSHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		document := map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": i.KeyID,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(i.signing.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.signing.PublicKey.E)).Bytes()),
			}},
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(document)
	})
}

// TokenOptions describes the credential to mint. Zero values fall back to a
// valid, currently active token signed by the published key.
type TokenOptions struct {
	Subject    string
	MerchantID string
	Audience   any
	Scopes     []string
	Issuer     string
	KeyID      string
	Algorithm  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	// OmitExpiry drops the exp claim entirely.
	OmitExpiry bool
	// ForgeSignature signs with a key the issuer never published.
	ForgeSignature bool
	// ExtraClaims are merged last and can override any claim above.
	ExtraClaims map[string]any
}

// Mint builds and signs a credential.
func (i *Issuer) Mint(options TokenOptions) (string, error) {
	algorithm := options.Algorithm
	if algorithm == "" {
		algorithm = "RS256"
	}
	hash, supported := algorithmHashes[algorithm]
	if !supported && algorithm != "none" {
		return "", errors.New("authtest: unsupported algorithm " + algorithm)
	}

	keyID := options.KeyID
	if keyID == "" {
		keyID = i.KeyID
	}
	header := map[string]any{"alg": algorithm, "typ": "JWT"}
	if algorithm != "none" {
		header["kid"] = keyID
	}

	issuedAt := options.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now()
	}
	expiresAt := options.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = issuedAt.Add(5 * time.Minute)
	}
	issuerURL := options.Issuer
	if issuerURL == "" {
		issuerURL = i.URL
	}
	subject := options.Subject
	if subject == "" {
		subject = "test-subject"
	}

	claims := map[string]any{
		"iss": issuerURL,
		"sub": subject,
		"iat": issuedAt.Unix(),
		"jti": "test-" + subject,
	}
	if !options.OmitExpiry {
		claims["exp"] = expiresAt.Unix()
	}
	if options.Audience != nil {
		claims["aud"] = options.Audience
	}
	if options.MerchantID != "" {
		claims["merchant_id"] = options.MerchantID
	}
	if len(options.Scopes) > 0 {
		claims["scope"] = strings.Join(options.Scopes, " ")
	}
	for name, value := range options.ExtraClaims {
		claims[name] = value
	}

	headerSegment, err := encodeSegment(header)
	if err != nil {
		return "", err
	}
	claimsSegment, err := encodeSegment(claims)
	if err != nil {
		return "", err
	}
	signingInput := headerSegment + "." + claimsSegment

	if algorithm == "none" {
		return signingInput + ".", nil
	}

	key := i.signing
	if options.ForgeSignature {
		key = i.unpublished
	}
	digest := hash.New()
	digest.Write([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, hash, digest.Sum(nil))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeSegment(value map[string]any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
