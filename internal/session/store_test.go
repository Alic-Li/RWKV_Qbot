package session

import (
	"qq-rwkv-bot/internal/rwkv"
	"testing"
	"time"
)

func TestIsolationEvictionAndReset(t *testing.T) {
	s := New(2, time.Hour)
	m := []rwkv.Message{{Role: "user", Content: "one"}}
	s.Set("c2c:u", m)
	m[0].Content = "mutated"
	if s.Get("c2c:u")[0].Content != "one" || s.Get("group:g:u") != nil {
		t.Fatal("isolation")
	}
	copy := s.Get("c2c:u")
	copy[0].Content = "mutated"
	if s.Get("c2c:u")[0].Content != "one" {
		t.Fatal("alias")
	}
	s.Set("group:g:u", m)
	s.Set("group:h:u", m)
	if s.Get("c2c:u") != nil {
		t.Fatal("eviction")
	}
	s.Reset("group:g:u")
	if s.Get("group:g:u") != nil {
		t.Fatal("reset")
	}
	s.mu.Lock()
	s.entries["group:h:u"] = entry{m, time.Now().Add(-2 * time.Hour)}
	s.mu.Unlock()
	if s.Get("group:h:u") != nil {
		t.Fatal("TTL")
	}
}
