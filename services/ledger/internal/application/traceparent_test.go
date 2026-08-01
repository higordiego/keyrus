package application

import (
	"errors"
	"testing"
)

func TestValidateTraceparent(t *testing.T) {
	t.Parallel()
	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if err := ValidateTraceparent(valid); err != nil {
		t.Fatalf("valid traceparent rejected: %v", err)
	}
	invalid := []string{
		"", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-03",
	}
	for _, value := range invalid {
		if err := ValidateTraceparent(value); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("invalid traceparent %q accepted: %v", value, err)
		}
	}
}
