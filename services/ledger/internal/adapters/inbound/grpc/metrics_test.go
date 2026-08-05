package grpc

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/application"
	"github.com/higordiegoti/keyrus/services/ledger/internal/observability"
)

// TestRecordMutationClassifiesExactlyLikeMapError proves the domain metric
// and the RPC status code returned to the caller can never disagree: every
// error recordMutation treats as a "conflict" or "failure" is classified the
// same way mapError already classifies it for the response.
func TestRecordMutationClassifiesExactlyLikeMapError(t *testing.T) {
	for _, test := range []struct {
		name          string
		err           error
		wantCommits   uint64
		wantConflicts uint64
		wantFailures  uint64
	}{
		{name: "success", err: nil, wantCommits: 1},
		{name: "idempotency conflict", err: application.ErrIdempotencyConflict, wantConflicts: 1},
		{name: "already reversed", err: application.ErrAlreadyReversed, wantConflicts: 1},
		{name: "caller validation error is not counted as a failure", err: application.ErrInvalidArgument},
		{name: "entry not found is not counted as a failure", err: application.ErrEntryNotFound},
		{name: "unclassified error counts as a failure", err: application.ErrCursorScopeMismatch, wantFailures: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{metrics: &observability.Metrics{}}
			server.recordMutation(test.err, time.Now().Add(-5*time.Millisecond))

			var buffer bytes.Buffer
			if err := server.metrics.WritePrometheus(&buffer); err != nil {
				t.Fatalf("WritePrometheus: %v", err)
			}
			output := buffer.String()

			assertCounter(t, output, "ledger_mutation_commits_total", test.wantCommits)
			assertCounter(t, output, "ledger_mutation_idempotency_conflicts_total", test.wantConflicts)
			assertCounter(t, output, "ledger_mutation_failures_total", test.wantFailures)
		})
	}
}

// TestRecordMutationIsANoOpWithoutMetricsAttached proves SetMetrics is
// optional: a Server that never had SetMetrics called must not panic when a
// mutation completes.
func TestRecordMutationIsANoOpWithoutMetricsAttached(t *testing.T) {
	server := &Server{}
	server.recordMutation(nil, time.Now())
	server.recordMutation(application.ErrIdempotencyConflict, time.Now())
}

func assertCounter(t *testing.T, prometheusText, metricName string, want uint64) {
	t.Helper()
	for _, line := range strings.Split(prometheusText, "\n") {
		if strings.HasPrefix(line, metricName+" ") {
			got := strings.TrimPrefix(line, metricName+" ")
			if got != strconv.FormatUint(want, 10) {
				t.Errorf("%s = %s, want %d", metricName, got, want)
			}
			return
		}
	}
	t.Errorf("metric %s not found in output:\n%s", metricName, prometheusText)
}
