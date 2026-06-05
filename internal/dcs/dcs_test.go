package dcs

import (
	"context"
	"testing"
)

func TestMemStore_KeyRoundTrip(t *testing.T) {
	s := NewMemStore()
	v, err := s.GetKey(context.Background(), "negotiation")
	if err != nil || v != "" {
		t.Fatalf("absent key should return (\"\", nil), got (%q, %v)", v, err)
	}
	ok, err := s.PutKeyIfAbsent(context.Background(), "negotiation", "abcdef")
	if err != nil || !ok {
		t.Fatalf("PutKeyIfAbsent into empty store should succeed, got (%v, %v)", ok, err)
	}
	ok2, err := s.PutKeyIfAbsent(context.Background(), "negotiation", "different")
	if err != nil || ok2 {
		t.Fatalf("second PutKeyIfAbsent should report (false, nil), got (%v, %v)", ok2, err)
	}
	v, _ = s.GetKey(context.Background(), "negotiation")
	if v != "abcdef" {
		t.Errorf("CAS should preserve the first writer, got %q", v)
	}
}

func TestMemStore_BootstrapLockExclusive(t *testing.T) {
	s := NewMemStore()
	rel, err := s.AcquireBootstrapLock(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("first lock acquire failed: %v", err)
	}
	if _, err := s.AcquireBootstrapLock(context.Background(), "owner-2"); err == nil {
		t.Error("second lock acquire should fail while the first is held")
	}
	rel()
	rel2, err := s.AcquireBootstrapLock(context.Background(), "owner-3")
	if err != nil {
		t.Fatalf("lock acquire after release failed: %v", err)
	}
	rel2()
}
