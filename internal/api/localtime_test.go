package api

import (
	"testing"
	"time"
)

func TestResolveLocalDateTimeDSTBoundaries(t *testing.T) {
	t.Run("nonexistent spring-forward time is rejected", func(t *testing.T) {
		if _, err := resolveLocalDateTime("2026-03-08T02:30:00", "America/New_York", ""); err != errNonexistentLocalTime {
			t.Fatalf("got %v, want nonexistent local time", err)
		}
	})
	t.Run("fall-back time requires an explicit fold", func(t *testing.T) {
		value := "2026-11-01T01:30:00"
		if _, err := resolveLocalDateTime(value, "America/New_York", ""); err != errAmbiguousLocalTime {
			t.Fatalf("got %v, want ambiguous local time", err)
		}
		earlier, err := resolveLocalDateTime(value, "America/New_York", "earlier")
		if err != nil {
			t.Fatal(err)
		}
		later, err := resolveLocalDateTime(value, "America/New_York", "later")
		if err != nil {
			t.Fatal(err)
		}
		if later.Sub(earlier) != time.Hour {
			t.Fatalf("fold instants differ by %s, want 1h", later.Sub(earlier))
		}
	})
	t.Run("unique local time resolves to its UTC instant", func(t *testing.T) {
		got, err := resolveLocalDateTime("2026-02-01T09:00:00", "Asia/Seoul", "")
		if err != nil {
			t.Fatal(err)
		}
		want := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("got %s, want %s", got, want)
		}
	})
}
