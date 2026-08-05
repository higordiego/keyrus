package runtimeobs

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestObserveDurationProducesCumulativeHistogramBuckets proves the hand
// rolled histogram behaves the way histogram_quantile() requires: every
// bucket whose upper bound is at or above an observation counts it, so a
// fast request is counted in every bucket, not just the smallest one it
// fits.
func TestObserveDurationProducesCumulativeHistogramBuckets(t *testing.T) {
	metrics := &Metrics{}
	request := httptest.NewRequest("GET", "/v1/entries", nil)

	metrics.Observe(request, 200, 30*time.Millisecond)  // fits every bucket
	metrics.Observe(request, 200, 750*time.Millisecond) // fits only 1s, 2.5s, 5s, +Inf

	var buffer strings.Builder
	metrics.writeDurationHistogram(&buffer, "ledger-api")
	output := buffer.String()

	cases := []struct {
		le   string
		want string
	}{
		{"0.05", "1"}, // only the 30ms observation
		{"0.5", "1"},  // still only the 30ms observation
		{"1", "2"},    // both observations
		{"+Inf", "2"},
	}
	for _, test := range cases {
		line := findBucketLine(output, test.le)
		if line == "" {
			t.Fatalf("bucket le=%q not found in:\n%s", test.le, output)
		}
		if !strings.HasSuffix(strings.TrimSpace(line), " "+test.want) {
			t.Errorf("bucket le=%q = %q, want count %s", test.le, line, test.want)
		}
	}

	if !strings.Contains(output, `cashflow_http_request_duration_seconds_count{service="ledger-api"} 2`) {
		t.Errorf("count line missing or wrong:\n%s", output)
	}
}

func findBucketLine(output, le string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, `le="`+le+`"`) {
			return line
		}
	}
	return ""
}
