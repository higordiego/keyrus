package reconciliation

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMetricsRecordRunReflectsMostRecentSuccessfulRun(t *testing.T) {
	m := &Metrics{}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	m.RecordRun(ReconcileResult{MissingEntries: 2, ExtraEntries: 1, DuplicatedEntries: 3, FinancialDifferenceMinor: 500}, 42, 150*time.Millisecond, nil, now)

	var buffer bytes.Buffer
	if err := m.WritePrometheus(&buffer); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	output := buffer.String()

	for metric, want := range map[string]string{
		"reconciliation_runs_total":                          "1",
		"reconciliation_errors_total":                        "0",
		"reconciliation_skipped_total":                       "0",
		"reconciliation_last_run_source_position_cut":        "42",
		"reconciliation_last_run_missing_entries":            "2",
		"reconciliation_last_run_extra_entries":              "1",
		"reconciliation_last_run_duplicated_entries":         "3",
		"reconciliation_last_run_financial_difference_minor": "500",
	} {
		if !containsMetricValue(output, metric, want) {
			t.Errorf("%s: want value %s, output:\n%s", metric, want, output)
		}
	}
}

func TestMetricsRecordRunCountsErrorsWithoutOverwritingLastGoodRun(t *testing.T) {
	m := &Metrics{}
	now := time.Now()
	m.RecordRun(ReconcileResult{MissingEntries: 7}, 10, time.Second, nil, now)
	m.RecordRun(ReconcileResult{}, 20, time.Second, errStreamFailedForTest, now.Add(time.Minute))

	var buffer bytes.Buffer
	if err := m.WritePrometheus(&buffer); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	output := buffer.String()

	if !containsMetricValue(output, "reconciliation_runs_total", "2") {
		t.Errorf("runs_total: output:\n%s", output)
	}
	if !containsMetricValue(output, "reconciliation_errors_total", "1") {
		t.Errorf("errors_total: output:\n%s", output)
	}
	// The failed run must not clobber the last successful run's gauges --
	// an operator diagnosing a stuck watermark needs the last real result,
	// not a zeroed-out one from the failed attempt.
	if !containsMetricValue(output, "reconciliation_last_run_missing_entries", "7") {
		t.Errorf("last_run_missing_entries should still reflect the last successful run: output:\n%s", output)
	}
	if !containsMetricValue(output, "reconciliation_last_run_source_position_cut", "10") {
		t.Errorf("last_run_source_position_cut should still reflect the last successful run: output:\n%s", output)
	}
}

var errStreamFailedForTest = errStreamFailed{}

type errStreamFailed struct{}

func (errStreamFailed) Error() string { return "stream failed" }

func containsMetricValue(prometheusText, metricName, want string) bool {
	for _, line := range strings.Split(prometheusText, "\n") {
		if strings.HasPrefix(line, metricName+" ") {
			return strings.TrimPrefix(line, metricName+" ") == want
		}
	}
	return false
}
