package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// MerchantRequirement declares whether the audience being protected expects a
// merchant-bound end user or a service identity.
type MerchantRequirement int

const (
	// MerchantRequired rejects tokens without a merchant claim. Public merchant
	// routes use it.
	MerchantRequired MerchantRequirement = iota
	// MerchantForbidden rejects tokens carrying a merchant claim. The private
	// gRPC surface uses it so an end-user token can never be replayed there.
	MerchantForbidden
)

// DefaultLeeway absorbs modest clock skew between Keycloak and the services.
const DefaultLeeway = 30 * time.Second

const (
	defaultMerchantClaim = "merchant_id"
	defaultScopeClaim    = "scope"
	minRSAModulusBits    = 2048
	maxTokenBytes        = 8192
)

// KeySource resolves the public key that signed a token.
type KeySource interface {
	VerificationKey(ctx context.Context, keyID string) (crypto.PublicKey, error)
}

// VerifierConfig is the full validation policy. Every field that widens trust
// must be set explicitly; there is no permissive default.
type VerifierConfig struct {
	// Issuer must equal the token iss claim exactly.
	Issuer string
	// Audience must appear in the token aud claim.
	Audience string
	// Keys resolves signing keys, normally a JWKSCache.
	Keys KeySource
	// Merchant declares whether a merchant claim is required or forbidden.
	Merchant MerchantRequirement
	// AllowedAlgorithms defaults to RS256 only. "none" and any HMAC algorithm
	// are rejected because only RSA keys are accepted for verification.
	AllowedAlgorithms []string
	// MerchantClaim defaults to merchant_id.
	MerchantClaim string
	// ScopeClaim defaults to scope.
	ScopeClaim string
	// Leeway defaults to DefaultLeeway.
	Leeway time.Duration
	// Now defaults to time.Now and exists so expiry is testable.
	Now func() time.Time
}

// Verifier validates issuer, audience, signature, expiry, scopes and the
// merchant binding of a bearer credential.
type Verifier struct {
	issuer        string
	audience      string
	keys          KeySource
	merchant      MerchantRequirement
	algorithms    map[string]crypto.Hash
	merchantClaim string
	scopeClaim    string
	leeway        time.Duration
	now           func() time.Time
}

var supportedAlgorithms = map[string]crypto.Hash{
	"RS256": crypto.SHA256,
	"RS384": crypto.SHA384,
	"RS512": crypto.SHA512,
}

// NewVerifier fails closed when the policy is incomplete.
func NewVerifier(config VerifierConfig) (*Verifier, error) {
	if config.Issuer == "" {
		return nil, errors.New("auth: issuer is required")
	}
	if config.Audience == "" {
		return nil, errors.New("auth: audience is required")
	}
	if config.Keys == nil {
		return nil, errors.New("auth: key source is required")
	}

	names := config.AllowedAlgorithms
	if len(names) == 0 {
		names = []string{"RS256"}
	}
	algorithms := make(map[string]crypto.Hash, len(names))
	for _, name := range names {
		hash, supported := supportedAlgorithms[name]
		if !supported {
			return nil, fmt.Errorf("auth: algorithm %q is not supported", name)
		}
		algorithms[name] = hash
	}

	verifier := &Verifier{
		issuer:        config.Issuer,
		audience:      config.Audience,
		keys:          config.Keys,
		merchant:      config.Merchant,
		algorithms:    algorithms,
		merchantClaim: config.MerchantClaim,
		scopeClaim:    config.ScopeClaim,
		leeway:        config.Leeway,
		now:           config.Now,
	}
	if verifier.merchantClaim == "" {
		verifier.merchantClaim = defaultMerchantClaim
	}
	if verifier.scopeClaim == "" {
		verifier.scopeClaim = defaultScopeClaim
	}
	if verifier.leeway == 0 {
		verifier.leeway = DefaultLeeway
	}
	if verifier.now == nil {
		verifier.now = time.Now
	}
	return verifier, nil
}

type joseHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type registeredClaims struct {
	Issuer          string       `json:"iss"`
	Subject         string       `json:"sub"`
	Audience        audienceList `json:"aud"`
	ExpiresAt       *numericDate `json:"exp"`
	NotBefore       *numericDate `json:"nbf"`
	IssuedAt        *numericDate `json:"iat"`
	TokenID         string       `json:"jti"`
	AuthorizedParty string       `json:"azp"`
}

// Verify establishes the identity or returns a sentinel rejection reason.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	if strings.TrimSpace(token) == "" {
		return Identity{}, ErrTokenMissing
	}
	if len(token) > maxTokenBytes {
		return Identity{}, ErrTokenMalformed
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrTokenMalformed
	}

	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return Identity{}, ErrTokenMalformed
	}
	var header joseHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Identity{}, ErrTokenMalformed
	}
	if header.Type != "" && header.Type != "JWT" && header.Type != "at+jwt" {
		return Identity{}, ErrTokenMalformed
	}
	hash, allowed := v.algorithms[header.Algorithm]
	if !allowed {
		return Identity{}, ErrAlgorithmNotAllowed
	}

	payloadBytes, err := decodeSegment(parts[1])
	if err != nil {
		return Identity{}, ErrTokenMalformed
	}
	signature, err := decodeSegment(parts[2])
	if err != nil {
		return Identity{}, ErrTokenMalformed
	}

	key, err := v.keys.VerificationKey(ctx, header.KeyID)
	if err != nil {
		return Identity{}, errors.Join(ErrKeyUnknown, err)
	}
	publicKey, isRSA := key.(*rsa.PublicKey)
	if !isRSA {
		return Identity{}, ErrAlgorithmNotAllowed
	}
	if publicKey.N.BitLen() < minRSAModulusBits {
		return Identity{}, ErrSignatureInvalid
	}

	digest := hash.New()
	digest.Write([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, hash, digest.Sum(nil), signature); err != nil {
		return Identity{}, ErrSignatureInvalid
	}

	return v.identityFromPayload(payloadBytes)
}

func (v *Verifier) identityFromPayload(payload []byte) (Identity, error) {
	var claims registeredClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, ErrTokenMalformed
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Identity{}, ErrTokenMalformed
	}

	if claims.Issuer != v.issuer {
		return Identity{}, ErrIssuerMismatch
	}
	if !claims.Audience.contains(v.audience) {
		return Identity{}, ErrAudienceMismatch
	}
	if claims.Subject == "" {
		return Identity{}, ErrSubjectMissing
	}
	if claims.ExpiresAt == nil {
		return Identity{}, ErrTokenMalformed
	}

	now := v.now()
	if now.After(claims.ExpiresAt.Time.Add(v.leeway)) {
		return Identity{}, ErrTokenExpired
	}
	if claims.NotBefore != nil && now.Add(v.leeway).Before(claims.NotBefore.Time) {
		return Identity{}, ErrTokenNotYetValid
	}

	merchantID, err := stringClaim(raw, v.merchantClaim)
	if err != nil {
		return Identity{}, err
	}
	switch v.merchant {
	case MerchantRequired:
		if merchantID == "" {
			return Identity{}, ErrMerchantMissing
		}
	case MerchantForbidden:
		if merchantID != "" {
			return Identity{}, ErrMerchantForbidden
		}
	}

	scopeValue, err := stringClaim(raw, v.scopeClaim)
	if err != nil {
		return Identity{}, err
	}

	identity := Identity{
		Subject:    claims.Subject,
		MerchantID: merchantID,
		ClientID:   claims.AuthorizedParty,
		Scopes:     ParseScopes(scopeValue),
		Issuer:     claims.Issuer,
		Audience:   claims.Audience,
		TokenID:    claims.TokenID,
		ExpiresAt:  claims.ExpiresAt.Time,
	}
	if claims.IssuedAt != nil {
		identity.IssuedAt = claims.IssuedAt.Time
	}
	return identity, nil
}

func stringClaim(raw map[string]json.RawMessage, name string) (string, error) {
	value, present := raw[name]
	if !present {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", ErrTokenMalformed
	}
	return text, nil
}

func decodeSegment(segment string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(segment)
}

// audienceList accepts both the string and array forms allowed by RFC 7519.
type audienceList []string

func (a *audienceList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audienceList{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audienceList) contains(audience string) bool {
	for _, value := range a {
		if value == audience {
			return true
		}
	}
	return false
}

// numericDate decodes the seconds-since-epoch form used by JWT time claims.
type numericDate struct {
	Time time.Time
}

func (n *numericDate) UnmarshalJSON(data []byte) error {
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return err
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return errors.New("auth: time claim is not a finite number")
	}
	whole, fraction := math.Modf(seconds)
	n.Time = time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC()
	return nil
}
