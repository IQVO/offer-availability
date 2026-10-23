package clock

import (
	"testing"
	"time"
)

func TestSystemNowIsUTCAndCurrent(t *testing.T) {
	before := time.Now()
	got := System{}.Now()
	after := time.Now()
	if got.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", got.Location())
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("now = %v, not between %v and %v", got, before, after)
	}
}
