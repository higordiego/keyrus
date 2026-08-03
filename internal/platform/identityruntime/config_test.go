package identityruntime

import (
	"testing"
	"time"
)

func TestParseDurationRequiresPositiveBudget(t *testing.T) {
	for _, raw := range []string{"0s", "-1s", "invalid"} {
		if _, err := ParseDuration(raw, time.Second); err == nil {
			t.Fatalf("ParseDuration(%q) accepted an invalid budget", raw)
		}
	}
	value, err := ParseDuration("10s", time.Second)
	if err != nil || value != 10*time.Second {
		t.Fatalf("ParseDuration returned %s, %v", value, err)
	}
}
