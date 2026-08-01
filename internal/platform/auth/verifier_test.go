package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
)

const (
	testIssuer     = "https://edge.cashflow.local/realms/cashflow"
	testAudience   = "cashflow-public-api"
	testMerchantID = "11111111-1111-4111-8111-111111111111"
)

func newIssuer(t *testing.T) *authtest.Issuer {
	t.Helper()
	issuer, err := authtest.NewIssuer(testIssuer)
	if err != nil {
		t.Fatalf("create test issuer: %v", err)
	}
	return issuer
}

func newVerifier(t *testing.T, issuer *authtest.Issuer, adjust func(*auth.VerifierConfig)) *auth.Verifier {
	t.Helper()
	config := auth.VerifierConfig{
		Issuer:   testIssuer,
		Audience: testAudience,
		Keys:     issuer.Keys(),
		Merchant: auth.MerchantRequired,
	}
	if adjust != nil {
		adjust(&config)
	}
	verifier, err := auth.NewVerifier(config)
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	return verifier
}

func mint(t *testing.T, issuer *authtest.Issuer, options authtest.TokenOptions) string {
	t.Helper()
	if options.Audience == nil {
		options.Audience = []string{testAudience}
	}
	if options.MerchantID == "" && !options.OmitExpiry {
		options.MerchantID = testMerchantID
	}
	token, err := issuer.Mint(options)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}

func TestVerifyDerivesMerchantAndScopesFromTheToken(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)
	verifier := newVerifier(t, issuer, nil)

	token := mint(t, issuer, authtest.TokenOptions{
		Subject: "merchant-a",
		Scopes:  []string{auth.ScopeLedgerRead, auth.ScopeLedgerWrite},
	})

	identity, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if identity.MerchantID != testMerchantID {
		t.Errorf("merchant: got %q, want %q", identity.MerchantID, testMerchantID)
	}
	if !identity.Scopes.HasAll(auth.ScopeLedgerRead, auth.ScopeLedgerWrite) {
		t.Errorf("scopes: got %v", identity.Scopes.Sorted())
	}
	if !identity.Owns(testMerchantID) {
		t.Error("identity does not own its own merchant")
	}
	if identity.Owns("22222222-2222-4222-8222-222222222222") {
		t.Error("identity claims to own another merchant")
	}
}

func TestVerifyRejectsEveryInvalidCredential(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)
	verifier := newVerifier(t, issuer, nil)
	past := time.Now().Add(-2 * time.Hour)

	cases := []struct {
		name  string
		token func() string
		want  error
	}{
		{
			name:  "absent",
			token: func() string { return "" },
			want:  auth.ErrTokenMissing,
		},
		{
			name:  "not a compact JWS",
			token: func() string { return "not-a-token" },
			want:  auth.ErrTokenMalformed,
		},
		{
			name: "expired",
			token: func() string {
				return mint(t, issuer, authtest.TokenOptions{IssuedAt: past, ExpiresAt: past.Add(time.Minute), MerchantID: testMerchantID})
			},
			want: auth.ErrTokenExpired,
		},
		{
			name:  "forged signature",
			token: func() string { return mint(t, issuer, authtest.TokenOptions{ForgeSignature: true}) },
			want:  auth.ErrSignatureInvalid,
		},
		{
			name:  "alg none",
			token: func() string { return mint(t, issuer, authtest.TokenOptions{Algorithm: "none"}) },
			want:  auth.ErrAlgorithmNotAllowed,
		},
		{
			name:  "unknown signing key",
			token: func() string { return mint(t, issuer, authtest.TokenOptions{KeyID: "rotated-away"}) },
			want:  auth.ErrKeyUnknown,
		},
		{
			name: "foreign issuer",
			token: func() string {
				return mint(t, issuer, authtest.TokenOptions{Issuer: "https://attacker.example/realms/cashflow"})
			},
			want: auth.ErrIssuerMismatch,
		},
		{
			name: "audience of another service",
			token: func() string {
				return mint(t, issuer, authtest.TokenOptions{Audience: []string{"cashflow-internal-api"}})
			},
			want: auth.ErrAudienceMismatch,
		},
		{
			name: "no merchant claim",
			token: func() string {
				token, err := issuer.Mint(authtest.TokenOptions{Audience: []string{testAudience}, Scopes: []string{auth.ScopeLedgerRead}})
				if err != nil {
					t.Fatalf("mint: %v", err)
				}
				return token
			},
			want: auth.ErrMerchantMissing,
		},
		{
			name: "no expiry claim",
			token: func() string {
				return mint(t, issuer, authtest.TokenOptions{OmitExpiry: true, MerchantID: testMerchantID})
			},
			want: auth.ErrTokenMalformed,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := verifier.Verify(context.Background(), testCase.token())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("got %v, want %v", err, testCase.want)
			}
			if !auth.IsAuthenticationFailure(err) {
				t.Errorf("%v was not classified as an authentication failure", err)
			}
		})
	}
}

func TestVerifyAcceptsTheStringFormOfAudience(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)
	verifier := newVerifier(t, issuer, nil)

	token := mint(t, issuer, authtest.TokenOptions{Audience: testAudience})
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("string audience was rejected: %v", err)
	}
}

func TestVerifyToleratesModestClockSkewButNotAnHour(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)
	now := time.Now()
	verifier := newVerifier(t, issuer, func(config *auth.VerifierConfig) {
		config.Now = func() time.Time { return now }
	})

	justExpired := mint(t, issuer, authtest.TokenOptions{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-10 * time.Second)})
	if _, err := verifier.Verify(context.Background(), justExpired); err != nil {
		t.Errorf("token inside the leeway was rejected: %v", err)
	}

	longExpired := mint(t, issuer, authtest.TokenOptions{IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)})
	if _, err := verifier.Verify(context.Background(), longExpired); !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("token past the leeway: got %v, want %v", err, auth.ErrTokenExpired)
	}
}

func TestInternalVerifierRefusesAMerchantToken(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)
	verifier := newVerifier(t, issuer, func(config *auth.VerifierConfig) {
		config.Audience = "cashflow-internal-api"
		config.Merchant = auth.MerchantForbidden
	})

	merchantToken := mint(t, issuer, authtest.TokenOptions{
		Audience:   []string{"cashflow-internal-api"},
		MerchantID: testMerchantID,
		Scopes:     []string{auth.ScopeLedgerInternal},
	})
	if _, err := verifier.Verify(context.Background(), merchantToken); !errors.Is(err, auth.ErrMerchantForbidden) {
		t.Fatalf("got %v, want %v", err, auth.ErrMerchantForbidden)
	}

	serviceToken, err := issuer.Mint(authtest.TokenOptions{
		Subject:  "service-account-cashflow-consolidation-svc",
		Audience: []string{"cashflow-internal-api"},
		Scopes:   []string{auth.ScopeLedgerInternal},
	})
	if err != nil {
		t.Fatalf("mint service token: %v", err)
	}
	identity, err := verifier.Verify(context.Background(), serviceToken)
	if err != nil {
		t.Fatalf("service token was rejected: %v", err)
	}
	if !identity.IsService() {
		t.Error("service identity reports a merchant")
	}
}

func TestNewVerifierRefusesAnIncompletePolicy(t *testing.T) {
	t.Parallel()
	issuer := newIssuer(t)

	cases := map[string]auth.VerifierConfig{
		"no issuer":           {Audience: testAudience, Keys: issuer.Keys()},
		"no audience":         {Issuer: testIssuer, Keys: issuer.Keys()},
		"no key source":       {Issuer: testIssuer, Audience: testAudience},
		"symmetric algorithm": {Issuer: testIssuer, Audience: testAudience, Keys: issuer.Keys(), AllowedAlgorithms: []string{"HS256"}},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := auth.NewVerifier(config); err == nil {
				t.Fatal("an incomplete verifier policy was accepted")
			}
		})
	}
}

func TestScopePolicyFailsClosedOnUndeclaredOperations(t *testing.T) {
	t.Parallel()
	policy := auth.PublicEdgePolicy()
	identity := auth.Identity{MerchantID: testMerchantID, Scopes: auth.ParseScopes("ledger:read ledger:write consolidation:read")}

	if err := policy.Authorize(auth.OperationCreateEntry, identity); err != nil {
		t.Errorf("granted scope was refused: %v", err)
	}
	if err := policy.Authorize("DELETE /v1/entries", identity); !errors.Is(err, auth.ErrOperationUnknown) {
		t.Errorf("undeclared operation: got %v, want %v", err, auth.ErrOperationUnknown)
	}
	if err := policy.Authorize(auth.OperationGetWatermark, identity); !errors.Is(err, auth.ErrOperationUnknown) {
		t.Errorf("the public policy must not know the internal RPC: got %v", err)
	}

	readOnly := auth.Identity{MerchantID: testMerchantID, Scopes: auth.ParseScopes("ledger:read")}
	if err := policy.Authorize(auth.OperationCreateEntry, readOnly); !errors.Is(err, auth.ErrScopeMissing) {
		t.Errorf("missing scope: got %v, want %v", err, auth.ErrScopeMissing)
	}
	if !auth.IsAuthorizationFailure(policy.Authorize(auth.OperationCreateEntry, readOnly)) {
		t.Error("a missing scope was not classified as an authorization failure")
	}
}

func TestScopeMatchingIsExactRatherThanPrefixed(t *testing.T) {
	t.Parallel()
	scopes := auth.ParseScopes("ledger:read consolidation:read")
	for _, near := range []string{"ledger", "ledger:", "ledger:readwrite", "edger:read", "ledger:read ", "LEDGER:READ"} {
		if scopes.Has(near) {
			t.Errorf("scope %q matched although it was never granted", near)
		}
	}
	if !scopes.Has(auth.ScopeLedgerRead) {
		t.Error("an exactly granted scope did not match")
	}
}
