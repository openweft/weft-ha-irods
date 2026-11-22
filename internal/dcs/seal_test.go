package dcs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	keyA  = "0123456789abcdef0123456789abcdef-A"
	keyB  = "0123456789abcdef0123456789abcdef-B"
	spath = "/weft/forgejo/x/secrets/secret_key"
	val   = "PLAINTEXT-SECRET-VALUE"
)

var ctx = context.Background()

func newSealed(t *testing.T, inner Store, cur string, prev ...string) *SealedStore {
	t.Helper()
	s, err := NewSealedStore(inner, cur, prev...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rawOf(t *testing.T, m *MemStore, p string) string {
	t.Helper()
	v, _ := m.GetKey(ctx, p)
	return v
}

func TestSealedRoundTripStoresNoPlaintext(t *testing.T) {
	m := NewMemStore()
	s := newSealed(t, m, keyA)
	if ok, err := s.PutKeyIfAbsent(ctx, spath, val); !ok || err != nil {
		t.Fatalf("put = %v, %v", ok, err)
	}
	raw := rawOf(t, m, spath)
	if strings.Contains(raw, val) || !strings.HasPrefix(raw, sealPrefix) {
		t.Fatalf("etcd holds %q", raw)
	}
	if got, err := s.GetKey(ctx, spath); got != val || err != nil {
		t.Fatalf("get = %q, %v", got, err)
	}
	if ok, _ := s.PutKeyIfAbsent(ctx, spath, "other"); ok {
		t.Fatal("second put overwrote")
	}
	if got, _ := s.GetKey(ctx, "/absent"); got != "" {
		t.Fatalf("absent = %q", got)
	}
}

func TestNonceIsFresh(t *testing.T) {
	m := NewMemStore()
	s := newSealed(t, m, keyA)
	s.PutKeyIfAbsent(ctx, "/a", val)
	s.PutKeyIfAbsent(ctx, "/b", val)
	if rawOf(t, m, "/a") == rawOf(t, m, "/b") {
		t.Fatal("same ciphertext twice")
	}
}

func TestLegacyPlaintextIsSealedAndIdempotent(t *testing.T) {
	m := NewMemStore()
	m.PutKeyIfAbsent(ctx, spath, val)
	s := newSealed(t, m, keyA)
	if got, err := s.GetKey(ctx, spath); got != val || err != nil {
		t.Fatalf("get = %q, %v", got, err)
	}
	first := rawOf(t, m, spath)
	if strings.Contains(first, val) || !strings.HasPrefix(first, sealPrefix) {
		t.Fatalf("not sealed: %q", first)
	}
	if got, _ := s.GetKey(ctx, spath); got != val || rawOf(t, m, spath) != first {
		t.Fatal("second read rewrote or changed the value")
	}
}

func TestRotationReSealsUnderCurrentKey(t *testing.T) {
	m := NewMemStore()
	newSealed(t, m, keyA).PutKeyIfAbsent(ctx, spath, val)
	before := rawOf(t, m, spath)
	s := newSealed(t, m, keyB, keyA)
	if got, err := s.GetKey(ctx, spath); got != val || err != nil {
		t.Fatalf("get = %q, %v", got, err)
	}
	after := rawOf(t, m, spath)
	if after == before {
		t.Fatal("not re-sealed")
	}
	// The old key is no longer needed.
	if got, err := newSealed(t, m, keyB).GetKey(ctx, spath); got != val || err != nil {
		t.Fatalf("new key alone: %q, %v", got, err)
	}
}

func TestReadErrorsNeverEchoASecret(t *testing.T) {
	m := NewMemStore()
	newSealed(t, m, keyA).PutKeyIfAbsent(ctx, spath, val)
	good := rawOf(t, m, spath)
	kid, body, _ := strings.Cut(strings.TrimPrefix(good, sealPrefix), ":")
	cases := map[string]struct{ stored, want string }{
		"no separator":  {sealPrefix + "abcd", "malformed"},
		"unknown key":   {sealPrefix + "deadbeef:" + body, "unknown key id"},
		"bad base64":    {sealPrefix + kid + ":!!!", "malformed"},
		"too short":     {sealPrefix + kid + ":QQ", "malformed"},
		"tampered":      {sealPrefix + kid + ":" + flip(body), "authenticate"},
		"moved ciphers": {good, "authenticate"}, // read below at another path
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mm := NewMemStore()
			p := spath
			if name == "moved ciphers" {
				p = "/elsewhere"
			}
			mm.PutKeyIfAbsent(ctx, p, c.stored)
			_, err := newSealed(t, mm, keyA).GetKey(ctx, p)
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), val) || strings.Contains(err.Error(), keyA) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func flip(b string) string {
	c := []byte(b)
	if c[len(c)-1] == 'A' {
		c[len(c)-1] = 'B'
	} else {
		c[len(c)-1] = 'A'
	}
	return string(c)
}

func TestNewSealedStoreRefusals(t *testing.T) {
	if _, err := NewSealedStore(plainStore{}, keyA); err == nil {
		t.Fatal("a store that cannot rewrite was accepted")
	}
	if _, err := NewSealedStore(NewMemStore(), "short"); err == nil || !strings.Contains(err.Error(), "current seal key") || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short current key: %v", err)
	}
	if _, err := NewSealedStore(NewMemStore(), keyA, "short"); err == nil || !strings.Contains(err.Error(), "previous seal key #1") {
		t.Fatalf("short previous key: %v", err)
	}
	// The boundary: 31 bytes refused, 32 accepted.
	if _, err := NewSealedStore(NewMemStore(), strings.Repeat("k", 31)); err == nil {
		t.Fatal("31-byte key accepted")
	}
	if _, err := NewSealedStore(NewMemStore(), strings.Repeat("k", 32)); err != nil {
		t.Fatalf("32-byte key refused: %v", err)
	}
}

// plainStore is a Store that is not a Rewriter.
type plainStore struct{ Store }

// failStore injects errors into a MemStore.
type failStore struct {
	*MemStore
	getErr, replaceErr, compactErr error
	replaceFalse                   bool
	compactCalls                   int
}

func (f *failStore) GetKey(c context.Context, p string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.MemStore.GetKey(c, p)
}

func (f *failStore) ReplaceKey(c context.Context, p, a, b string) (bool, error) {
	if f.replaceErr != nil {
		return false, f.replaceErr
	}
	if f.replaceFalse {
		return false, nil
	}
	return f.MemStore.ReplaceKey(c, p, a, b)
}

func (f *failStore) Compact(context.Context) error {
	f.compactCalls++
	return f.compactErr
}

func TestStoreErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	f := &failStore{MemStore: NewMemStore(), getErr: boom}
	if _, err := newSealed(t, f, keyA).GetKey(ctx, spath); !errors.Is(err, boom) {
		t.Fatalf("get: %v", err)
	}
	f = &failStore{MemStore: NewMemStore(), replaceErr: boom}
	f.MemStore.PutKeyIfAbsent(ctx, spath, val)
	if _, err := newSealed(t, f, keyA).GetKey(ctx, spath); !errors.Is(err, boom) {
		t.Fatalf("replace: %v", err)
	}
}

func TestLostSwapRaceGivesUp(t *testing.T) {
	f := &failStore{MemStore: NewMemStore(), replaceFalse: true}
	f.MemStore.PutKeyIfAbsent(ctx, spath, val)
	if _, err := newSealed(t, f, keyA).GetKey(ctx, spath); err == nil || strings.Contains(err.Error(), val) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompactionFailureIsRetriedAndNotFatal(t *testing.T) {
	f := &failStore{MemStore: NewMemStore(), compactErr: errors.New("denied")}
	f.MemStore.PutKeyIfAbsent(ctx, spath, val)
	s := newSealed(t, f, keyA)
	if got, err := s.GetKey(ctx, spath); got != val || err != nil {
		t.Fatalf("get = %q, %v", got, err)
	}
	f.compactErr = nil
	s.GetKey(ctx, spath) // retried on the next read
	s.GetKey(ctx, spath) // and not again once done
	if f.compactCalls != 2 {
		t.Fatalf("compactCalls = %d", f.compactCalls)
	}
}

type badRand struct{}

func (badRand) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestEntropyFailureIsAnError(t *testing.T) {
	old := randReader
	randReader = badRand{}
	defer func() { randReader = old }()
	m := NewMemStore()
	s := newSealed(t, m, keyA)
	if _, err := s.PutKeyIfAbsent(ctx, spath, val); err == nil {
		t.Fatal("put without entropy succeeded")
	}
	m.PutKeyIfAbsent(ctx, spath, val)
	if _, err := s.GetKey(ctx, spath); err == nil {
		t.Fatal("legacy migration without entropy succeeded")
	}
	if rawOf(t, m, spath) != val {
		t.Fatal("value changed despite the failure")
	}
}

func TestMemStoreReplaceKey(t *testing.T) {
	m := NewMemStore()
	if ok, _ := m.ReplaceKey(ctx, "/k", "a", "b"); ok {
		t.Fatal("replaced an absent key")
	}
	m.PutKeyIfAbsent(ctx, "/k", "a")
	if ok, _ := m.ReplaceKey(ctx, "/k", "x", "b"); ok {
		t.Fatal("replaced on a wrong previous value")
	}
	if ok, _ := m.ReplaceKey(ctx, "/k", "a", "b"); !ok || rawOf(t, m, "/k") != "b" {
		t.Fatal("did not replace")
	}
	if err := m.Compact(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMustPanicsOnError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("must swallowed an error")
		}
	}()
	must(0, errors.New("x"))
}
