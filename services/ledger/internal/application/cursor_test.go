package application

import (
	"errors"
	"testing"
)

func TestCursorCodecRejectsTampering(t *testing.T) {
	t.Parallel()
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.encode(cursorPayload{Version: 1, MerchantID: "merchant", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := codec.decode(tampered); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expected invalid cursor, got %v", err)
	}
}
