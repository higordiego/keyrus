package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

func TestReconciliationStreamRequiresBothInternalAndOperationalScopes(t *testing.T) {
	policy := auth.InternalGRPCPolicy()
	for _, test := range []struct {
		name   string
		scopes []string
		want   error
	}{
		{name: "both scopes", scopes: []string{auth.ScopeLedgerInternal, auth.ScopeOpsReconcile}},
		{name: "internal only", scopes: []string{auth.ScopeLedgerInternal}, want: auth.ErrScopeMissing},
		{name: "operations only", scopes: []string{auth.ScopeOpsReconcile}, want: auth.ErrScopeMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := auth.Identity{Scopes: auth.ParseScopes(strings.Join(test.scopes, " "))}
			err := policy.Authorize(auth.OperationStreamEntriesAtCut, identity)
			if !errors.Is(err, test.want) {
				t.Fatalf("authorize returned %v, want %v", err, test.want)
			}
		})
	}
}

func TestReconciliationOpsPolicyRequiresOpsReconcileScope(t *testing.T) {
	policy := auth.ReconciliationOpsPolicy()
	for _, test := range []struct {
		name   string
		scopes []string
		want   error
	}{
		{name: "ops:reconcile granted", scopes: []string{auth.ScopeOpsReconcile}},
		{name: "unrelated scope only", scopes: []string{auth.ScopeLedgerInternal}, want: auth.ErrScopeMissing},
		{name: "no scopes", scopes: nil, want: auth.ErrScopeMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := auth.Identity{Scopes: auth.ParseScopes(strings.Join(test.scopes, " "))}
			err := policy.Authorize(auth.OperationReprocessDLQ, identity)
			if !errors.Is(err, test.want) {
				t.Fatalf("authorize returned %v, want %v", err, test.want)
			}
		})
	}
}

func TestReconciliationOpsPolicyRefusesUndeclaredOperations(t *testing.T) {
	policy := auth.ReconciliationOpsPolicy()
	identity := auth.Identity{Scopes: auth.ParseScopes(auth.ScopeOpsReconcile)}
	if err := policy.Authorize(auth.OperationGetWatermark, identity); !errors.Is(err, auth.ErrOperationUnknown) {
		t.Fatalf("authorize returned %v, want %v", err, auth.ErrOperationUnknown)
	}
}
