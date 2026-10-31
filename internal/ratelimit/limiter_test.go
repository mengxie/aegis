package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucketRefillsOverTime(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(func() time.Time { return now })
	for range 2 {
		if ok, _ := l.Allow("key", 2); !ok {
			t.Fatal("burst should be allowed")
		}
	}
	ok, wait := l.Allow("key", 2)
	if ok || wait != 30*time.Second {
		t.Fatalf("third request: ok=%v wait=%v, want refusal for 30s", ok, wait)
	}
	if ok, _ := l.Allow("other", 2); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(30 * time.Second)
	if ok, _ := l.Allow("key", 2); !ok {
		t.Fatal("a token should have refilled")
	}
}
