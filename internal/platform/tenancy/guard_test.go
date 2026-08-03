package tenancy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/tenancy"
)

const (
	merchantA = "11111111-1111-4111-8111-111111111111"
	merchantB = "22222222-2222-4222-8222-222222222222"
)

// directory is an in-memory owner lookup. It carries no financial data: the
// guard only ever needs to know which merchant owns an identifier.
type directory map[string]string

func (d directory) OwnerOf(_ context.Context, resourceID string) (string, bool, error) {
	owner, found := d[resourceID]
	return owner, found, nil
}

func newGuard(t *testing.T, owners directory) tenancy.Guard {
	t.Helper()
	guard, err := tenancy.NewGuard(owners)
	if err != nil {
		t.Fatalf("create guard: %v", err)
	}
	return guard
}

func TestGuardAdmitsOnlyTheOwningMerchant(t *testing.T) {
	t.Parallel()
	guard := newGuard(t, directory{"entry-owned-by-a": merchantA})
	identity := auth.Identity{MerchantID: merchantA}

	if err := guard.Authorize(context.Background(), identity, "entry-owned-by-a"); err != nil {
		t.Fatalf("owner was refused its own resource: %v", err)
	}
}

func TestGuardReturnsTheSameRefusalForForeignAndAbsentResources(t *testing.T) {
	t.Parallel()
	guard := newGuard(t, directory{"entry-owned-by-b": merchantB})
	identity := auth.Identity{MerchantID: merchantA}

	foreign := guard.Authorize(context.Background(), identity, "entry-owned-by-b")
	absent := guard.Authorize(context.Background(), identity, "entry-that-never-existed")

	if !errors.Is(foreign, tenancy.ErrResourceUnavailable) {
		t.Errorf("foreign resource: got %v, want %v", foreign, tenancy.ErrResourceUnavailable)
	}
	if !errors.Is(absent, tenancy.ErrResourceUnavailable) {
		t.Errorf("absent resource: got %v, want %v", absent, tenancy.ErrResourceUnavailable)
	}
	if foreign.Error() != absent.Error() {
		t.Errorf("refusals differ and would let a caller tell existence apart: %q vs %q", foreign, absent)
	}
}

func TestGuardRefusesAnIdentityWithoutAMerchant(t *testing.T) {
	t.Parallel()
	guard := newGuard(t, directory{"entry-owned-by-a": merchantA})

	// A service identity has no merchant. It must not inherit tenant access by
	// matching an empty owner.
	err := guard.Authorize(context.Background(), auth.Identity{}, "entry-owned-by-a")
	if !errors.Is(err, tenancy.ErrResourceUnavailable) {
		t.Fatalf("got %v, want %v", err, tenancy.ErrResourceUnavailable)
	}
}

func TestGuardPropagatesLookupFailuresInsteadOfDenying(t *testing.T) {
	t.Parallel()
	lookupErr := errors.New("directory unavailable")
	guard, err := tenancy.NewGuard(failingResolver{err: lookupErr})
	if err != nil {
		t.Fatalf("create guard: %v", err)
	}

	got := guard.Authorize(context.Background(), auth.Identity{MerchantID: merchantA}, "entry")
	if !errors.Is(got, lookupErr) {
		t.Fatalf("a storage failure was reported as a refusal: got %v", got)
	}
}

func TestNewGuardRequiresAResolver(t *testing.T) {
	t.Parallel()
	if _, err := tenancy.NewGuard(nil); err == nil {
		t.Fatal("a guard without an owner resolver was accepted")
	}
}

type failingResolver struct{ err error }

func (f failingResolver) OwnerOf(context.Context, string) (string, bool, error) {
	return "", false, f.err
}
