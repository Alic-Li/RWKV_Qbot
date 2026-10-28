package session

import (
	"qq-rwkv-bot/internal/rwkv"
	"sync"
	"time"
)

type entry struct {
	messages []rwkv.Message
	used     time.Time
}
type Store struct {
	mu      sync.Mutex
	entries map[string]entry
	limit   int
	ttl     time.Duration
}

func New(limit int, ttl time.Duration) *Store {
	return &Store{entries: make(map[string]entry), limit: limit, ttl: ttl}
}
func (s *Store) Get(key string) []rwkv.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key]
	if time.Since(e.used) > s.ttl {
		delete(s.entries, key)
		return nil
	}
	return append([]rwkv.Message(nil), e.messages...)
}
func (s *Store) Set(key string, m []rwkv.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.entries {
		if now.Sub(e.used) > s.ttl {
			delete(s.entries, k)
		}
	}
	if _, ok := s.entries[key]; !ok && len(s.entries) >= s.limit {
		var oldest string
		var t time.Time
		for k, e := range s.entries {
			if t.IsZero() || e.used.Before(t) {
				oldest = k
				t = e.used
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[key] = entry{append([]rwkv.Message(nil), m...), now}
}
func (s *Store) Reset(key string) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.entries, key) }
