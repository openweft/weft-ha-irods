// Package dcs is the Distributed Configuration Store layer — etcd
// today, but the interface stays implementation-agnostic so the
// reconcile loop tests can swap a fake in.
//
// dcs has TWO responsibilities for weft-ha-irods (smaller scope than
// weft-ha-postgresql's, because iRODS providers are stateless once
// the iCAT is in place — no continuous leader election) :
//
//  1. Zone-key store : the bootstrap leader writes
//     `negotiation_key` / `control_plane_key` / `zone_key` under
//     `/weft/irods/<zone>/keys/...` ; the other providers read
//     them on boot and install them in their server config. The
//     keys never change after bootstrap.
//  2. Bootstrap advisory lock : the first provider to come up holds
//     a lease-bound lock and does the iCAT schema creation +
//     key minting. The other two wait for the lock holder to
//     release, then see the keys already in place.
package dcs

import (
	"context"
	"errors"
)

// Store is the minimum surface the reconcile loop needs from a
// distributed configuration store. Implementations are expected to
// be safe for concurrent use.
type Store interface {
	// GetKey returns the zone key under `path` or ("", nil) when
	// absent. Returns a wrapped error on transport failure.
	GetKey(ctx context.Context, path string) (string, error)
	// PutKeyIfAbsent sets `path` to `value` only when it doesn't
	// already exist (compare-and-set). Returns (true, nil) on
	// successful set, (false, nil) when another provider raced us,
	// (false, err) on transport failure.
	PutKeyIfAbsent(ctx context.Context, path, value string) (bool, error)
	// AcquireBootstrapLock blocks until either the agent holds the
	// bootstrap lock or ctx expires. The returned `release` func
	// MUST be called once the bootstrap work is done (or has
	// determined no work is needed).
	AcquireBootstrapLock(ctx context.Context, owner string) (release func(), err error)
	// Close releases any underlying resources.
	Close() error
}

// ErrNotImplemented signals that a Store method has not been wired
// for the current build. Used by the in-memory scaffold below for
// methods that would need a real etcd to behave correctly.
var ErrNotImplemented = errors.New("dcs: not implemented in scaffold build")

// MemStore is a process-local stand-in for the etcd-backed store.
// It exists so the reconcile loop has SOMETHING to talk to in unit
// tests + single-node smoke tests. Bootstrap-lock acquisition
// always succeeds immediately ; key set/get persist for the
// process's lifetime.
type MemStore struct {
	keys   map[string]string
	locked bool
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{keys: map[string]string{}}
}

// GetKey implements Store.
func (m *MemStore) GetKey(_ context.Context, path string) (string, error) {
	return m.keys[path], nil
}

// PutKeyIfAbsent implements Store.
func (m *MemStore) PutKeyIfAbsent(_ context.Context, path, value string) (bool, error) {
	if _, ok := m.keys[path]; ok {
		return false, nil
	}
	m.keys[path] = value
	return true, nil
}

// AcquireBootstrapLock implements Store. The MemStore version
// enforces local exclusion only — for cross-process locks the
// caller must wire an etcd-backed Store.
func (m *MemStore) AcquireBootstrapLock(_ context.Context, _ string) (func(), error) {
	if m.locked {
		return func() {}, errors.New("memstore bootstrap lock already held in-process")
	}
	m.locked = true
	return func() { m.locked = false }, nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }
