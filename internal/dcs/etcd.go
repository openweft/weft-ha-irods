// etcd.go — the production Store backed by go.etcd.io/etcd/client/v3.
//
// The iRODS HA agents need TWO things from the DCS :
//
//   - A CAS-style key store : the bootstrap leader writes the three
//     zone keys (negotiation, control_plane, zone) under
//     `/weft/irods/<zone>/keys/...` exactly once ; followers
//     read them on boot and install them in their server config.
//   - A lease-bound advisory lock around the bootstrap step itself :
//     concurrency.NewMutex on `/weft/irods/<zone>/bootstrap-lock`
//     guarantees only one provider mints the keys + creates the iCAT
//     schema. The session's TTL means a fenced bootstrap leader
//     releases the lock automatically.
//
// This file is paid only when WEFT_HA_IRODS_ETCD is set — the
// MemStore in dcs.go remains the dev/test default.

package dcs

import (
	"context"
	"fmt"
	"path"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// EtcdStore implements Store against an external etcd cluster.
//
// One Store instance per agent process. The session lazy-opens on
// first use ; closing the Store drops the lease so any lock the
// process held is released within session TTL.
type EtcdStore struct {
	endpoints  []string
	zoneName   string
	sessionTTL int

	mu      sync.Mutex
	client  *clientv3.Client
	session *concurrency.Session
}

// NewEtcdStore wires a Store against the given etcd endpoints. The
// zoneName is used as the key prefix so multiple iRODS zones can
// share the same etcd cluster. sessionTTL is in seconds and defaults
// to 15 if non-positive — matches what weft-ha-postgresql uses for
// its leader-election lease.
func NewEtcdStore(endpoints []string, zoneName string, sessionTTL int) *EtcdStore {
	if sessionTTL <= 0 {
		sessionTTL = 15
	}
	return &EtcdStore{
		endpoints:  endpoints,
		zoneName:   zoneName,
		sessionTTL: sessionTTL,
	}
}

// connect lazy-opens the client + session. The session owns the lease
// that lock keys hang off — losing it drops the lock automatically
// (which is what we want when the agent is fenced).
func (e *EtcdStore) connect(ctx context.Context) (*clientv3.Client, *concurrency.Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.session != nil {
		return e.client, e.session, nil
	}
	if e.client == nil {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   e.endpoints,
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("etcd client: %w", err)
		}
		e.client = cli
	}
	sess, err := concurrency.NewSession(e.client,
		concurrency.WithTTL(e.sessionTTL),
		concurrency.WithContext(ctx),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("etcd session: %w", err)
	}
	e.session = sess
	return e.client, e.session, nil
}

// GetKey implements Store. Returns ("", nil) for an absent key —
// matches the MemStore contract the bootstrap package relies on.
func (e *EtcdStore) GetKey(ctx context.Context, key string) (string, error) {
	cli, _, err := e.connect(ctx)
	if err != nil {
		return "", err
	}
	resp, err := cli.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("etcd Get(%s): %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return "", nil
	}
	return string(resp.Kvs[0].Value), nil
}

// PutKeyIfAbsent implements Store using a single-shot Txn comparing
// CreateRevision to zero. Returns (true, nil) when this call inserted
// the value, (false, nil) when a peer already wrote it.
func (e *EtcdStore) PutKeyIfAbsent(ctx context.Context, key, value string) (bool, error) {
	cli, _, err := e.connect(ctx)
	if err != nil {
		return false, err
	}
	resp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, value)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("etcd Txn(%s): %w", key, err)
	}
	return resp.Succeeded, nil
}

// bootstrapLockPath returns the etcd key the bootstrap mutex lives at.
func (e *EtcdStore) bootstrapLockPath() string {
	return path.Join("/weft/irods", e.zoneName, "bootstrap-lock")
}

// AcquireBootstrapLock implements Store using concurrency.Mutex. The
// returned release closure unlocks the mutex ; it does NOT close the
// session (the agent keeps the same session for subsequent CAS writes
// after key minting completes).
//
// `owner` is intentionally unused for diagnostics today — etcd's
// concurrency.Mutex carries the session lease ID, which uniquely
// identifies the holder.
func (e *EtcdStore) AcquireBootstrapLock(ctx context.Context, _ string) (func(), error) {
	_, sess, err := e.connect(ctx)
	if err != nil {
		return nil, err
	}
	mu := concurrency.NewMutex(sess, e.bootstrapLockPath())
	if err := mu.Lock(ctx); err != nil {
		return nil, fmt.Errorf("acquire bootstrap lock: %w", err)
	}
	return func() {
		// Use a fresh background context so a cancelled caller ctx
		// doesn't leak the lock until session expiry.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mu.Unlock(releaseCtx)
	}, nil
}

// Close releases the session (which drops the lease + any lock held by
// it) and closes the etcd client. Idempotent.
func (e *EtcdStore) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var firstErr error
	if e.session != nil {
		if err := e.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.session = nil
	}
	if e.client != nil {
		if err := e.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.client = nil
	}
	return firstErr
}

// compile-time assertion : EtcdStore implements Store.
var _ Store = (*EtcdStore)(nil)
