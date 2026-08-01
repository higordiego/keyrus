package domain

import "time"

type DailyBalance struct {
	MerchantID          string
	BusinessDate        time.Time
	CreditsMinor        int64
	DebitsMinor         int64
	NetMinor            int64
	EntryCount          int64
	ClosingBalanceMinor int64
	Version             int64
}

type MerchantProgress struct {
	MerchantID      string
	SourcePosition  int64
	AppliedPosition int64
	FirstGap        *int64
	UpdatedAt       time.Time
}

func (p MerchantProgress) HasGap() bool {
	return p.FirstGap != nil || p.AppliedPosition != p.SourcePosition
}
