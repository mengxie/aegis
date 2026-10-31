// Package ratelimit provides per-key token buckets.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows up to N requests per minute per key, with bursts of up to N.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

func New(now func() time.Time) *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, now: now}
}

// Allow consumes one token for key. When denied, it returns how long until a token is available.
func (l *Limiter) Allow(key string, perMinute int) (bool, time.Duration) {
	if perMinute <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	capacity := float64(perMinute)
	ratePerSecond := capacity / 60
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*ratePerSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / ratePerSecond * float64(time.Second))
	return false, wait
}
