package store

import (
	"testing"
	"time"
)

// SetNow replaces the clock used for writes until the test ends.
func SetNow(t testing.TB, f func() time.Time) {
	t.Helper()
	prev := utcNow
	utcNow = f
	t.Cleanup(func() { utcNow = prev })
}
