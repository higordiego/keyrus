// Package timingoracle decides whether two response-time populations leak the
// existence of a resource. It is deliberately a pure function so the criterion
// itself can be exercised with synthetic populations that must be rejected.
package timingoracle

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// MinSamples keeps the rank statistic meaningful. Below this the criterion
	// cannot distinguish a real channel from measurement noise.
	MinSamples = 16

	// AbsoluteTolerance is the measurement floor for this deployment: the edge
	// plus the adapter answer both 404 classes in single-digit milliseconds, so
	// a median gap above this is a practical signal rather than scheduler noise.
	AbsoluteTolerance = 3 * time.Millisecond

	// RelativeTolerance bounds the gap as a fraction of the faster class, so the
	// criterion does not silently widen when the whole system slows down.
	RelativeTolerance = 0.10

	// MaxSeparability caps how well a single observation may classify the two
	// populations. The rank statistic below equals the probability that a random
	// sample of one class exceeds a random sample of the other, so 0.25 means an
	// attacker's single-sample classifier stays at or below 75% accuracy.
	MaxSeparability = 0.25
)

type Verdict struct {
	Samples       int
	ForeignMedian time.Duration
	AbsentMedian  time.Duration
	ForeignMAD    time.Duration
	AbsentMAD     time.Duration
	Difference    time.Duration
	Tolerance     time.Duration
	Separability  float64
}

func (v Verdict) String() string {
	return fmt.Sprintf("paired samples=%d per class; median foreign=%s absent=%s; MAD foreign=%s absent=%s; "+
		"|delta|=%s <= tolerance=%s (max of %s and %.0f%% of the faster median); "+
		"rank separability=%.3f <= %.3f (single-sample classifier accuracy <= %.0f%%)",
		v.Samples, v.ForeignMedian, v.AbsentMedian, v.ForeignMAD, v.AbsentMAD,
		v.Difference, v.Tolerance, AbsoluteTolerance, RelativeTolerance*100,
		v.Separability, MaxSeparability, (0.5+MaxSeparability)*100)
}

// Evaluate rejects two populations whenever their central tendency differs by
// more than the pre-declared budget, or whenever their ranks are separable
// enough for a single observation to classify the resource. Both criteria are
// required: a stable pair of populations can be perfectly separable while its
// medians sit inside any noise-derived tolerance.
func Evaluate(foreign, absent []time.Duration) (Verdict, error) {
	if len(foreign) < MinSamples || len(absent) < MinSamples {
		return Verdict{}, fmt.Errorf("timingoracle: need at least %d samples per class, got foreign=%d absent=%d",
			MinSamples, len(foreign), len(absent))
	}
	foreignMedian, absentMedian := median(foreign), median(absent)
	verdict := Verdict{
		Samples:       min(len(foreign), len(absent)),
		ForeignMedian: foreignMedian,
		AbsentMedian:  absentMedian,
		ForeignMAD:    medianAbsoluteDeviation(foreign, foreignMedian),
		AbsentMAD:     medianAbsoluteDeviation(absent, absentMedian),
		Difference:    absDuration(foreignMedian - absentMedian),
		Tolerance:     tolerance(foreignMedian, absentMedian),
		Separability:  math.Abs(rankDominance(foreign, absent) - 0.5),
	}
	if verdict.Difference > verdict.Tolerance {
		return verdict, fmt.Errorf("timingoracle: median gap is a practical enumeration channel: %s", verdict)
	}
	if verdict.Separability > MaxSeparability {
		return verdict, fmt.Errorf("timingoracle: response times classify the resource class: %s", verdict)
	}
	return verdict, nil
}

// tolerance is anchored on the faster class so a slower system cannot buy a
// larger absolute budget for the same relative leak.
func tolerance(left, right time.Duration) time.Duration {
	relative := time.Duration(RelativeTolerance * float64(min(left, right)))
	if relative > AbsoluteTolerance {
		return relative
	}
	return AbsoluteTolerance
}

// rankDominance is the Mann-Whitney statistic normalised to [0,1]: the
// probability that a random foreign sample exceeds a random absent sample, with
// ties counted as half. Indistinguishable populations sit at 0.5; perfectly
// ordered populations reach 0 or 1.
func rankDominance(foreign, absent []time.Duration) float64 {
	wins := 0.0
	for _, left := range foreign {
		for _, right := range absent {
			switch {
			case left > right:
				wins++
			case left == right:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(foreign)*len(absent))
}

func median(values []time.Duration) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}

func medianAbsoluteDeviation(values []time.Duration, center time.Duration) time.Duration {
	deviations := make([]time.Duration, len(values))
	for index, value := range values {
		deviations[index] = absDuration(value - center)
	}
	return median(deviations)
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
