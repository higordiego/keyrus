package forwardingoracle

import (
	"strings"
	"testing"
)

func TestUnchangedEntrypointIsAccepted(t *testing.T) {
	t.Parallel()
	if err := AssertNotForwarded(42, 42); err != nil {
		t.Fatalf("an unmoved entrypoint counter was rejected: %v", err)
	}
}

// This is the review's exact bypass, reproduced against the pure decision
// function: the edge forwards an invalid identity, defense in depth at the
// adapter catches it, and a naive post-auth counter would stay flat. The
// pre-auth entrypoint counter this oracle reads does move, and the oracle must
// fail loudly instead of passing.
func TestForwardedCallWithDefenseInDepthIsRejected(t *testing.T) {
	t.Parallel()
	err := AssertNotForwarded(7, 8)
	if err == nil {
		t.Fatal("a forwarded call that only defense in depth caught was accepted")
	}
	if !strings.Contains(err.Error(), "forwarded the call") {
		t.Fatalf("unexpected rejection reason: %v", err)
	}
}

func TestDecreasedCounterIsAlsoRejected(t *testing.T) {
	t.Parallel()

	if err := AssertNotForwarded(10, 9); err == nil {
		t.Fatal("a decreased entrypoint counter was accepted")
	}
}
