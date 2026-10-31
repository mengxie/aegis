package provider

import (
	"testing"
	"time"
)

func TestBreakerOpensThenProbesOnce(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(3, 30*time.Second, func() time.Time { return now })
	for range 3 {
		if !b.Allow() {
			t.Fatal("closed breaker refused")
		}
		b.Failure()
	}
	if b.Allow() || !b.Open() {
		t.Fatal("breaker should be open after 3 failures")
	}
	now = now.Add(31 * time.Second)
	if !b.Allow() {
		t.Fatal("half-open breaker should let one probe through")
	}
	if b.Allow() {
		t.Fatal("only one probe at a time")
	}
	b.Success()
	if !b.Allow() || b.Open() {
		t.Fatal("successful probe should close the breaker")
	}
}
