package api

import (
	"testing"
	"time"
)

func TestAnalyticsWindowBuildsMatchingComparisons(t *testing.T) {
	now := time.Date(2025, time.March, 15, 18, 30, 0, 0, time.UTC)

	day, ok := analyticsWindow("day", now)
	if !ok || !day.currentStart.Equal(time.Date(2025, time.February, 14, 0, 0, 0, 0, time.UTC)) ||
		!day.previousEnd.Equal(time.Date(2025, time.February, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected daily comparison window: %#v", day)
	}

	month, ok := analyticsWindow("month", now)
	if !ok || !month.currentStart.Equal(time.Date(2024, time.March, 16, 0, 0, 0, 0, time.UTC)) ||
		!month.previousEnd.Equal(time.Date(2024, time.March, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected monthly comparison window: %#v", month)
	}

	year, ok := analyticsWindow("year", now)
	if !ok || !year.currentStart.Equal(time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)) ||
		!year.previousStart.Equal(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)) ||
		!year.previousEnd.Equal(time.Date(2024, time.March, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected yearly comparison window: %#v", year)
	}
	if _, ok := analyticsWindow("decade", now); ok {
		t.Fatal("unsupported analytics period was accepted")
	}
}

func TestPercentChangeHandlesEmptyComparison(t *testing.T) {
	if got := percentChange(0, 0); got != 0 {
		t.Fatalf("percentChange(0, 0) = %v, want 0", got)
	}
	if got := percentChange(4, 0); got != 100 {
		t.Fatalf("percentChange(4, 0) = %v, want 100", got)
	}
	if got := percentChange(75, 100); got != -25 {
		t.Fatalf("percentChange(75, 100) = %v, want -25", got)
	}
}
