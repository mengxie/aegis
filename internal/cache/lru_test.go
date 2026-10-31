package cache

import (
	"testing"
	"time"
)

func TestLRUExpiresAndEvicts(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string](2, func() time.Time { return now })
	c.Put("a", "1", time.Minute)
	c.Put("b", "2", time.Minute)
	c.Get("a")
	c.Put("c", "3", time.Minute) // evicts b, the least recently used
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should have expired")
	}
}
