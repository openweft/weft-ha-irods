package dcs

import (
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type fakeTxn struct {
	clientv3.Txn
	ok  bool
	err error
}

func (f *fakeTxn) If(...clientv3.Cmp) clientv3.Txn  { return f }
func (f *fakeTxn) Then(...clientv3.Op) clientv3.Txn { return f }
func (f *fakeTxn) Commit() (*clientv3.TxnResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &clientv3.TxnResponse{Succeeded: f.ok}, nil
}

type fakeKV struct {
	txn        *fakeTxn
	getErr     error
	compactErr error
	compacted  int64
}

func (f *fakeKV) Txn(context.Context) clientv3.Txn { return f.txn }
func (f *fakeKV) Get(context.Context, string, ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &clientv3.GetResponse{Header: &pb.ResponseHeader{Revision: 42}}, nil
}
func (f *fakeKV) Compact(_ context.Context, rev int64, _ ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	f.compacted = rev
	return nil, f.compactErr
}

func storeWith(kv *fakeKV) *EtcdStore {
	e := NewEtcdStore(nil, "inst", 15)
	e.kvOverride = kv
	return e
}

func TestEtcdReplaceKey(t *testing.T) {
	for _, ok := range []bool{true, false} {
		e := storeWith(&fakeKV{txn: &fakeTxn{ok: ok}})
		if got, err := e.ReplaceKey(ctx, "/k", "a", "b"); got != ok || err != nil {
			t.Fatalf("ok=%v got %v, %v", ok, got, err)
		}
	}
	boom := errors.New("boom")
	if _, err := storeWith(&fakeKV{txn: &fakeTxn{err: boom}}).ReplaceKey(ctx, "/k", "a", "b"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestEtcdCompactToHead(t *testing.T) {
	kv := &fakeKV{}
	if err := storeWith(kv).Compact(ctx); err != nil || kv.compacted != 42 {
		t.Fatalf("err=%v rev=%d", err, kv.compacted)
	}
	boom := errors.New("boom")
	if err := storeWith(&fakeKV{getErr: boom}).Compact(ctx); !errors.Is(err, boom) {
		t.Fatalf("get: %v", err)
	}
	if err := storeWith(&fakeKV{compactErr: boom}).Compact(ctx); !errors.Is(err, boom) {
		t.Fatalf("compact: %v", err)
	}
}

func TestEtcdKVConnectFailure(t *testing.T) {
	e := NewEtcdStore(nil, "inst", 15) // no endpoints: the client refuses at once
	if _, err := e.ReplaceKey(ctx, "/k", "a", "b"); err == nil {
		t.Fatal("ReplaceKey connected to nothing")
	}
	if err := e.Compact(ctx); err == nil {
		t.Fatal("Compact connected to nothing")
	}
}
