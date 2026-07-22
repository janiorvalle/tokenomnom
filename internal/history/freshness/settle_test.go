package freshness

import (
	"testing"
	"time"
)

func TestSettleWindowBoundaryIsInclusive(t *testing.T) {
	asOf := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if !activeWithinSettleWindow(asOf.Add(-SettleWindow), asOf) {
		t.Fatal("source exactly at settle-window boundary was not active")
	}
	if activeWithinSettleWindow(asOf.Add(-SettleWindow-time.Nanosecond), asOf) {
		t.Fatal("source older than settle-window boundary was active")
	}
	if !activeWithinSettleWindow(asOf.Add(SettleWindow), asOf) {
		t.Fatal("source exactly at future settle-window boundary was not active")
	}
	if activeWithinSettleWindow(asOf.Add(SettleWindow+time.Nanosecond), asOf) {
		t.Fatal("source beyond future settle-window boundary was active")
	}
}
