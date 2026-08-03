package tracecontext

import "testing"

func TestSanitizeTraceStateUsesADataFreeWholeValueAllowlist(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		input string
		want  string
	}{
		"fixed public marker":               {PublicTraceState, PublicTraceState},
		"surrounding whitespace":            {"  " + PublicTraceState + "  ", PublicTraceState},
		"empty":                             {"", ""},
		"arbitrary vendor":                  {"vendor=opaque", ""},
		"allowed plus attacker entry":       {PublicTraceState + ",attacker=secret", ""},
		"sensitive but syntactically valid": {"cashflow=e2e-command-key-e2e-sensitive-description-987654321", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeTraceState(test.input); got != test.want {
				t.Fatalf("SanitizeTraceState(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
