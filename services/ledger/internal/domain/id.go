package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ID is a UUID-shaped identifier. The domain does not depend on a UUID library.
type ID string

func ParseID(value string) (ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", ErrInvalidID
	}
	compact := value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 {
		return "", ErrInvalidID
	}
	allZero := true
	for _, value := range decoded {
		if value != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "", ErrInvalidID
	}
	return ID(value), nil
}

func (id ID) String() string { return string(id) }

// NewUUIDv7 returns a time-ordered UUIDv7 suitable for database keys.
func NewUUIDv7(now time.Time) (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate uuid entropy: %w", err)
	}
	millis := uint64(now.UTC().UnixMilli())
	raw[0] = byte(millis >> 40)
	raw[1] = byte(millis >> 32)
	raw[2] = byte(millis >> 24)
	raw[3] = byte(millis >> 16)
	raw[4] = byte(millis >> 8)
	raw[5] = byte(millis)
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	return ID(value), nil
}
