package domain

import (
	"fmt"
	"time"
)

const (
	DateLayout          = "2006-01-02"
	MaximumBackdateDays = 30
	DefaultMerchantZone = "America/Fortaleza"
)

type BusinessDate struct {
	value time.Time
}

func ParseBusinessDate(value string) (BusinessDate, error) {
	parsed, err := time.Parse(DateLayout, value)
	if err != nil {
		return BusinessDate{}, fmt.Errorf("parse business date: %w", ErrInvalidBusinessDate)
	}
	return BusinessDate{value: parsed}, nil
}

func BusinessDateFromTime(value time.Time) BusinessDate {
	year, month, day := value.Date()
	return BusinessDate{value: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

func (d BusinessDate) String() string                 { return d.value.Format(DateLayout) }
func (d BusinessDate) Time() time.Time                { return d.value }
func (d BusinessDate) Before(other BusinessDate) bool { return d.value.Before(other.value) }
func (d BusinessDate) After(other BusinessDate) bool  { return d.value.After(other.value) }

type Calendar struct {
	location *time.Location
}

func NewCalendar(zone string) (Calendar, error) {
	if zone == "" {
		zone = DefaultMerchantZone
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return Calendar{}, fmt.Errorf("load time zone %q: %w", zone, ErrInvalidTimeZone)
	}
	return Calendar{location: location}, nil
}

func (c Calendar) Today(now time.Time) BusinessDate {
	return BusinessDateFromTime(now.In(c.location))
}

func (c Calendar) Resolve(now time.Time, requested *BusinessDate) (BusinessDate, error) {
	today := c.Today(now)
	if requested == nil {
		return today, nil
	}
	earliest := BusinessDateFromTime(today.value.AddDate(0, 0, -MaximumBackdateDays))
	if requested.Before(earliest) || requested.After(today) {
		return BusinessDate{}, ErrInvalidBusinessDate
	}
	return *requested, nil
}
