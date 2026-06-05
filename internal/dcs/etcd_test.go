package dcs

import "testing"

// TestEtcdStoreCompilesAgainstStore is a smoke compile-time check that
// EtcdStore satisfies the Store interface. Integration tests against
// a real or embedded etcd live in a follow-up milestone (see
// weft-ha-postgresql/internal/dcs/dcs_integration_test.go for the
// pattern) ; pulling embed.Etcd into this module would add a heavy
// dep that we don't need yet.
func TestEtcdStoreCompilesAgainstStore(t *testing.T) {
	var s Store = NewEtcdStore([]string{"127.0.0.1:2379"}, "weftZone", 15)
	if s == nil {
		t.Fatal("NewEtcdStore returned nil")
	}
}
