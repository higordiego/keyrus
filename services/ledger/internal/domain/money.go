package domain

import (
	"fmt"
	"strconv"
	"strings"
)

const CurrencyBRL = "BRL"

// Money stores a positive amount in the smallest BRL unit. It never uses float.
type Money struct {
	minor int64
}

func NewMoney(amountMinor int64, currency string) (Money, error) {
	if currency != CurrencyBRL {
		return Money{}, ErrInvalidCurrency
	}
	if amountMinor <= 0 {
		return Money{}, ErrInvalidAmount
	}
	return Money{minor: amountMinor}, nil
}

// ParseBRL accepts a decimal representation with exactly zero, one, or two
// fractional digits and rejects implicit rounding.
func ParseBRL(value string) (Money, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return Money{}, ErrInvalidAmount
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 2)) {
		return Money{}, ErrInvalidAmount
	}
	if !asciiDigits(parts[0]) || (len(parts) == 2 && !asciiDigits(parts[1])) {
		return Money{}, ErrInvalidAmount
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return Money{}, ErrInvalidAmount
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fractionText := parts[1]
		if len(fractionText) == 1 {
			fractionText += "0"
		}
		fraction, err = strconv.ParseInt(fractionText, 10, 64)
		if err != nil {
			return Money{}, ErrInvalidAmount
		}
	}
	if whole > (int64(^uint64(0)>>1)-fraction)/100 {
		return Money{}, ErrInvalidAmount
	}
	return NewMoney(whole*100+fraction, CurrencyBRL)
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (m Money) AmountMinor() int64 { return m.minor }
func (m Money) Currency() string   { return CurrencyBRL }

func (m Money) String() string {
	return fmt.Sprintf("%d.%02d", m.minor/100, m.minor%100)
}
