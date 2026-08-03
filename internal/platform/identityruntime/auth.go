package identityruntime

import (
	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

func Verifier(issuer, audience, jwksURL, caFile string, merchant auth.MerchantRequirement) (*auth.Verifier, error) {
	client, err := HTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	keys, err := auth.NewJWKSCache(auth.JWKSConfig{Endpoint: jwksURL, Client: client})
	if err != nil {
		return nil, err
	}
	return auth.NewVerifier(auth.VerifierConfig{
		Issuer:   issuer,
		Audience: audience,
		Keys:     keys,
		Merchant: merchant,
	})
}
