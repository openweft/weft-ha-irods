package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/openweft/weft-ha-irods/internal/config"
	"github.com/openweft/weft-ha-irods/internal/dcs"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestPickStoreSealsEtcd(t *testing.T) {
	t.Setenv("WEFT_HA_IRODS_ETCD", "")
	cfg := config.Config{ZoneName: "z", EtcdEndpoints: []string{"https://127.0.0.1:1"}, SealKey: strings.Repeat("k", 32)}
	s, err := pickStore(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*dcs.SealedStore); !ok {
		t.Fatalf("store is %T, want a sealed store", s)
	}
}

func TestPickStoreRefusesAShortKey(t *testing.T) {
	t.Setenv("WEFT_HA_IRODS_ETCD", "")
	cfg := config.Config{ZoneName: "z", EtcdEndpoints: []string{"https://127.0.0.1:1"}, SealKey: "short"}
	if _, err := pickStore(cfg, quiet); err == nil {
		t.Fatal("a short seal key was accepted")
	}
}

func TestPickStoreRefusesPlaintextEtcd(t *testing.T) {
	t.Setenv("WEFT_HA_IRODS_ETCD", "")
	cfg := config.Config{ZoneName: "z", EtcdEndpoints: []string{"http://127.0.0.1:1"}, SealKey: strings.Repeat("k", 32)}
	if _, err := pickStore(cfg, quiet); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("a plaintext etcd was accepted: %v", err)
	}
	// The environment override goes through the same check.
	t.Setenv("WEFT_HA_IRODS_ETCD", "etcd:2379")
	cfg.EtcdEndpoints = []string{"https://127.0.0.1:1"}
	if _, err := pickStore(cfg, quiet); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("a plaintext etcd from the environment was accepted: %v", err)
	}
	cfg.EtcdInsecure = true
	if _, err := pickStore(cfg, quiet); err != nil {
		t.Fatalf("the explicit flag was refused: %v", err)
	}
}

func TestPickStoreWithoutEtcdIsMemory(t *testing.T) {
	t.Setenv("WEFT_HA_IRODS_ETCD", "")
	s, err := pickStore(config.Config{}, quiet)
	if _, ok := s.(*dcs.MemStore); !ok || err != nil {
		t.Fatalf("store %T, err %v", s, err)
	}
}

func TestRunAgentStopsOnABadSealKey(t *testing.T) {
	t.Setenv("WEFT_HA_IRODS_ETCD", "")
	cfg := config.Config{NodeName: "n", ZoneName: "z", EtcdEndpoints: []string{"https://127.0.0.1:1"}, SealKey: "short"}
	// A regression would start a real agent: bound it instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runAgent(ctx, cfg, time.Second); err == nil || !strings.Contains(err.Error(), "seal key") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentRefusesAnUnreadableSealKeyReference(t *testing.T) {
	root := rootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"agent", "--etcd-seal-key-from", "nosuchscheme:x"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "--etcd-seal-key-from") {
		t.Fatalf("err = %v", err)
	}
}

func TestSealFlagsExist(t *testing.T) {
	agent, _, err := rootCmd().Find([]string{"agent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"etcd-seal-key-from", "etcd-seal-old-key-from", "etcd-client-key-from",
		"etcd-password-from", "etcd-ca", "etcd-cert", "etcd-user", "etcd-insecure-plaintext"} {
		if agent.Flags().Lookup(n) == nil {
			t.Errorf("--%s missing", n)
		}
	}
}
