// seal.go — SealedStore keeps every shared secret in etcd as AEAD
// ciphertext, so an etcd read, snapshot, backup or data directory yields no
// plaintext secret.
//
// Cluster key: each node obtains the same sealing key through its own
// secretsource reference (--etcd-seal-key-from, e.g. systemd-creds:etcd-seal).
// The key itself is never written to etcd. The 32-byte AES key is derived from
// the secret with HKDF-SHA256 (crypto/hkdf), the cipher is AES-256-GCM
// (crypto/cipher): no invented primitive.
//
// Stored value:  weft-seal:v1:<key id>:<base64url(nonce || ciphertext)>
// The additional data of the AEAD is the etcd path, so a sealed value cannot
// be moved to another key. The key id is a truncated HMAC of the key (it
// reveals nothing usable) and selects the key at read time, which makes
// rotation possible: start nodes with the new key as current and the old one
// as previous; every value sealed under a previous key is re-sealed under the
// current one on first read.
//
// Migration: a value without the prefix is a legacy plaintext. On read it is
// sealed and swapped in with a compare-and-swap on its value (idempotent and
// safe against concurrent nodes), then the etcd history is compacted so the
// superseded plaintext revision is dropped.
package dcs

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
)

const (
	sealPrefix = "weft-seal:v1:"
	// MinSealKeyLen is the shortest sealing secret accepted, in bytes.
	MinSealKeyLen = 32
	// maxSwapAttempts bounds the re-read loop when peers swap concurrently.
	maxSwapAttempts = 5
)

// randReader is replaced by tests.
var randReader io.Reader = rand.Reader

// Rewriter is what a Store must offer for SealedStore to migrate and rotate:
// a compare-and-swap on the value, and a compaction of the key history.
type Rewriter interface {
	// ReplaceKey sets path to next only if its current value is prev.
	ReplaceKey(ctx context.Context, path, prev, next string) (bool, error)
	// Compact drops superseded revisions so replaced values leave no trace.
	Compact(ctx context.Context) error
}

func must[T any](v T, err error) T {
	if err != nil {
		panic("dcs: " + err.Error())
	}
	return v
}

type sealKey struct {
	id   string
	aead cipher.AEAD
}

func newSealKey(secret string) (*sealKey, error) {
	if len(secret) < MinSealKeyLen {
		return nil, fmt.Errorf("seal key must be at least %d bytes", MinSealKeyLen)
	}
	// None of these can fail for a 32-byte output and key (their only errors
	// are an oversized output or a key of the wrong size): must panics on a
	// programming error rather than carrying dead error paths.
	raw := must(hkdf.Key(sha256.New, []byte(secret), nil, "weft-ha etcd seal v1", 32))
	aead := must(cipher.NewGCM(must(aes.NewCipher(raw))))
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte("key id"))
	clear(raw)
	return &sealKey{id: hex.EncodeToString(mac.Sum(nil)[:4]), aead: aead}, nil
}

// SealedStore wraps a Store so that values are sealed before they reach it.
type SealedStore struct {
	Store
	rw      Rewriter
	current *sealKey
	keys    map[string]*sealKey
	// compactPending is true while a rewrite has not yet been compacted.
	compactPending atomic.Bool
}

// NewSealedStore seals values under current; previous keys stay readable (and
// are migrated away from). inner must implement Rewriter.
func NewSealedStore(inner Store, current string, previous ...string) (*SealedStore, error) {
	rw, ok := inner.(Rewriter)
	if !ok {
		return nil, errors.New("dcs: store cannot rewrite keys (no ReplaceKey/Compact), refusing to seal")
	}
	cur, err := newSealKey(current)
	if err != nil {
		return nil, fmt.Errorf("current seal key: %w", err)
	}
	s := &SealedStore{Store: inner, rw: rw, current: cur, keys: map[string]*sealKey{cur.id: cur}}
	for i, p := range previous {
		k, err := newSealKey(p)
		if err != nil {
			return nil, fmt.Errorf("previous seal key #%d: %w", i+1, err)
		}
		s.keys[k.id] = k
	}
	return s, nil
}

func (k *sealKey) seal(path, plain string) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return "", fmt.Errorf("seal nonce: %w", err)
	}
	ct := k.aead.Seal(nonce, nonce, []byte(plain), []byte(path))
	return sealPrefix + k.id + ":" + base64.RawURLEncoding.EncodeToString(ct), nil
}

// open returns the plaintext and the id of the key that sealed it.
func (s *SealedStore) open(path, stored string) (plain, kid string, err error) {
	rest := strings.TrimPrefix(stored, sealPrefix)
	kid, body, ok := strings.Cut(rest, ":")
	if !ok {
		return "", "", fmt.Errorf("sealed value at %s is malformed", path)
	}
	k := s.keys[kid]
	if k == nil {
		return "", "", fmt.Errorf("value at %s is sealed with unknown key id %s (pass the old key with --etcd-seal-old-key-from)", path, kid)
	}
	ct, err := base64.RawURLEncoding.DecodeString(body)
	ns := k.aead.NonceSize()
	if err != nil || len(ct) < ns {
		return "", "", fmt.Errorf("sealed value at %s is malformed", path)
	}
	pt, err := k.aead.Open(nil, ct[:ns], ct[ns:], []byte(path))
	if err != nil {
		return "", "", fmt.Errorf("value at %s does not authenticate (wrong key or tampered)", path)
	}
	return string(pt), kid, nil
}

// GetKey returns the plaintext at path, sealing a legacy plaintext or a value
// sealed under a previous key on the way.
func (s *SealedStore) GetKey(ctx context.Context, path string) (string, error) {
	for range maxSwapAttempts {
		stored, err := s.Store.GetKey(ctx, path)
		if err != nil || stored == "" {
			return "", err
		}
		plain, kid := stored, ""
		if strings.HasPrefix(stored, sealPrefix) {
			if plain, kid, err = s.open(path, stored); err != nil {
				return "", err
			}
			if kid == s.current.id {
				s.compactIfPending(ctx)
				return plain, nil
			}
		}
		sealed, err := s.current.seal(path, plain)
		if err != nil {
			return "", err
		}
		swapped, err := s.rw.ReplaceKey(ctx, path, stored, sealed)
		if err != nil {
			return "", err
		}
		if swapped {
			s.compactPending.Store(true)
			s.compactIfPending(ctx)
			return plain, nil
		}
		// A peer changed the value first: read again.
	}
	return "", fmt.Errorf("value at %s kept changing while it was being sealed", path)
}

// compactIfPending drops the superseded revisions. A failure is not fatal (the
// secret is already sealed in the live keyspace) and is retried on the next read.
func (s *SealedStore) compactIfPending(ctx context.Context) {
	if !s.compactPending.Load() {
		return
	}
	if err := s.rw.Compact(ctx); err != nil {
		slog.Warn("etcd compaction failed; superseded values remain in the history until it succeeds", "err", err)
		return
	}
	s.compactPending.Store(false)
}

// PutKeyIfAbsent seals value and stores it only when path is absent.
func (s *SealedStore) PutKeyIfAbsent(ctx context.Context, path, value string) (bool, error) {
	sealed, err := s.current.seal(path, value)
	if err != nil {
		return false, err
	}
	return s.Store.PutKeyIfAbsent(ctx, path, sealed)
}
