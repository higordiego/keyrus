package timingoracle

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

const samples = 32

func TestStableTwoFoldSeparationIsRejected(t *testing.T) {
	t.Parallel()
	// The exact counterexample from the review: both classes are perfectly
	// stable, so every noise-derived tolerance collapses to zero, yet a single
	// observation separates them.
	verdict, err := Evaluate(constant(100*time.Millisecond, samples), constant(200*time.Millisecond, samples))
	if err == nil {
		t.Fatalf("deterministic 100ms/200ms populations were accepted: %s", verdict)
	}
	if !strings.Contains(err.Error(), "practical enumeration channel") {
		t.Fatalf("unexpected rejection reason: %v", err)
	}
	if verdict.Separability != 0.5 {
		t.Fatalf("rank separability: got %.3f, want 0.5", verdict.Separability)
	}
}

func TestTwoFoldSeparationWithInflatedVarianceIsRejected(t *testing.T) {
	t.Parallel()
	// Inflating the spread used to widen the tolerance past the real gap.
	random := rand.New(rand.NewSource(7))
	foreign := jittered(random, 100*time.Millisecond, 40*time.Millisecond, samples)
	absent := jittered(random, 200*time.Millisecond, 40*time.Millisecond, samples)
	if verdict, err := Evaluate(foreign, absent); err == nil {
		t.Fatalf("noisy 100ms/200ms populations were accepted: %s", verdict)
	}
}

func TestSmallButPerfectlyConsistentOffsetIsRejected(t *testing.T) {
	t.Parallel()
	// The median gap sits well inside the tolerance, so only the rank criterion
	// can catch this channel.
	foreign := constant(10*time.Millisecond, samples)
	absent := constant(10*time.Millisecond+200*time.Microsecond, samples)
	verdict, err := Evaluate(foreign, absent)
	if err == nil {
		t.Fatalf("perfectly ordered populations were accepted: %s", verdict)
	}
	if !strings.Contains(err.Error(), "classify the resource class") {
		t.Fatalf("unexpected rejection reason: %v", err)
	}
	if verdict.Difference > verdict.Tolerance {
		t.Fatalf("this case must be rejected by the rank criterion alone: %s", verdict)
	}
}

func TestIndistinguishablePopulationsAreAccepted(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(11))
	foreign := jittered(random, 7*time.Millisecond, 2*time.Millisecond, samples)
	absent := jittered(random, 7*time.Millisecond, 2*time.Millisecond, samples)
	verdict, err := Evaluate(foreign, absent)
	if err != nil {
		t.Fatalf("indistinguishable populations were rejected: %v", err)
	}
	if verdict.Samples != samples {
		t.Fatalf("verdict sample count: got %d, want %d", verdict.Samples, samples)
	}
}

func TestUnderSampledPopulationsAreRejected(t *testing.T) {
	t.Parallel()
	if _, err := Evaluate(constant(time.Millisecond, MinSamples-1), constant(time.Millisecond, samples)); err == nil {
		t.Fatal("an under-sampled population was accepted")
	}
}

func constant(value time.Duration, count int) []time.Duration {
	population := make([]time.Duration, count)
	for index := range population {
		population[index] = value
	}
	return population
}

func jittered(random *rand.Rand, center, spread time.Duration, count int) []time.Duration {
	population := make([]time.Duration, count)
	for index := range population {
		population[index] = center + time.Duration(random.Int63n(int64(2*spread))) - spread
	}
	return population
}
