// Package cache is a small, concurrency-safe LRU cache with per-entry expiry.
package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry[V any] struct {
	key     string
	value   V
	expires time.Time
}

type LRU[V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
	now      func() time.Time
}

func New[V any](capacity int, now func() time.Time) *LRU[V] {
	return &LRU[V]{capacity: capacity, items: map[string]*list.Element{}, order: list.New(), now: now}
}

func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	element, ok := c.items[key]
	if !ok {
		return zero, false
	}
	e := element.Value.(*entry[V])
	if !c.now().Before(e.expires) {
		c.order.Remove(element)
		delete(c.items, key)
		return zero, false
	}
	c.order.MoveToFront(element)
	return e.value, true
}

func (c *LRU[V]) Put(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.Remove(element)
	}
	c.items[key] = c.order.PushFront(&entry[V]{key: key, value: value, expires: c.now().Add(ttl)})
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*entry[V]).key)
	}
}

func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
