// Package tenancy enforces that an authenticated merchant only reaches its own
// resources, and that a refusal is indistinguishable from an absent resource.
package tenancy

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

// ErrResourceUnavailable is returned both when a resource does not exist and
// when it belongs to another merchant. Callers must map it to a single
// response so the caller cannot enumerate identifiers.
var ErrResourceUnavailable = errors.New("tenancy: resource is unavailable")

// OwnerResolver answers which merchant owns a resource identifier. It is
// implemented by each service over its own schema; this package holds no
// persistence and no financial rule.
type OwnerResolver interface {
	// OwnerOf returns the owning merchant and whether the resource exists.
	OwnerOf(ctx context.Context, resourceID string) (merchantID string, found bool, err error)
}

// Guard resolves ownership and refuses everything that is not the caller's.
type Guard struct {
	resolver OwnerResolver
}

// NewGuard fails closed when no resolver is supplied.
func NewGuard(resolver OwnerResolver) (Guard, error) {
	if resolver == nil {
		return Guard{}, errors.New("tenancy: owner resolver is required")
	}
	return Guard{resolver: resolver}, nil
}

// Authorize permits access only when the resource exists and the authenticated
// identity owns it.
//
// The lookup and the comparison run on both paths and the comparison operands
// are padded to a common length, so an absent identifier and a foreign one cost
// the same work and return the same error. That keeps @SCN-RNF06-003 honest:
// the response reveals nothing about existence, and timing does not either.
func (g Guard) Authorize(ctx context.Context, identity auth.Identity, resourceID string) error {
	owner, found, err := g.resolver.OwnerOf(ctx, resourceID)
	if err != nil {
		return err
	}

	width := len(identity.MerchantID)
	if len(owner) > width {
		width = len(owner)
	}
	matches := subtle.ConstantTimeCompare(padded(identity.MerchantID, width), padded(owner, width)) == 1

	if !found || !matches || identity.MerchantID == "" {
		return ErrResourceUnavailable
	}
	return nil
}

func padded(value string, width int) []byte {
	buffer := make([]byte, width)
	copy(buffer, value)
	return buffer
}
