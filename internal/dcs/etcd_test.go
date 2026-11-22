package dcs

import "testing"

// TestEtcdStoreCompilesAgainstStore is a smoke compile-time check that
// EtcdStore satisfies the Store interface. The TLS and authentication paths
// are exercised against a TLS gRPC fake of the etcd services in
// security_test.go (no embedded etcd: it would add a heavy dependency).
func TestEtcdStoreCompilesAgainstStore(t *testing.T) {
	e, err := NewEtcdStore([]string{"https://127.0.0.1:2379"}, "smoke-install", 15, EtcdSecurity{})
	if err != nil {
		t.Fatal(err)
	}
	var s Store = e
	if s == nil {
		t.Fatal("NewEtcdStore returned nil")
	}
}
