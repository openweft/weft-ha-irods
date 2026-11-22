package sealflag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func fakeOpen(t *testing.T, m map[string]string) {
	t.Helper()
	old := open
	t.Cleanup(func() { open = old })
	open = func(_ context.Context, ref string) ([]byte, error) {
		v, ok := m[ref]
		if !ok {
			return nil, errors.New("no such reference")
		}
		return []byte(v), nil
	}
}

func cmdFor() *cobra.Command {
	return &cobra.Command{Use: "x", SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return nil }}
}

func TestAddFromResolvesAndTrims(t *testing.T) {
	fakeOpen(t, map[string]string{"r:a": "s3cret\r\n"})
	var dst string
	cmd := cmdFor()
	b := AddFrom(cmd, "key", &dst, "a key")
	cmd.SetArgs([]string{"--key-from", "r:a"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(context.Background(), b); err != nil || dst != "s3cret" {
		t.Fatalf("got %q, %v", dst, err)
	}
}

func TestAddFromHasNoPlainTwin(t *testing.T) {
	var dst string
	cmd := cmdFor()
	AddFrom(cmd, "key", &dst, "a key")
	if cmd.Flags().Lookup("key") != nil {
		t.Fatal("a plain --key flag exists: its value would show in ps")
	}
}

func TestUnsetLeavesDestination(t *testing.T) {
	dst := "kept"
	cmd := cmdFor()
	b := AddFrom(cmd, "key", &dst, "a key")
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(context.Background(), b); err != nil || dst != "kept" {
		t.Fatalf("got %q, %v", dst, err)
	}
}

func TestEmptyAndMissingAreErrorsWithoutValue(t *testing.T) {
	fakeOpen(t, map[string]string{"r:empty": "\n", "r:ok": "xyz"})
	for _, ref := range []string{"r:empty", "r:none"} {
		var dst string
		cmd := cmdFor()
		b := AddFrom(cmd, "key", &dst, "a key")
		cmd.SetArgs([]string{"--key-from", ref})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		err := Resolve(context.Background(), b)
		if err == nil || !strings.Contains(err.Error(), "--key-from") {
			t.Fatalf("%s: err = %v", ref, err)
		}
		if (ref == "r:empty") != errors.Is(err, errEmpty) {
			t.Fatalf("%s: wrong cause: %v", ref, err)
		}
		if ref == "r:none" && !strings.Contains(err.Error(), "no such reference") {
			t.Fatalf("the open error was lost: %v", err)
		}
	}
}

func TestDefaultOpenRefusesAnUnknownScheme(t *testing.T) {
	var dst string
	cmd := cmdFor()
	b := AddFrom(cmd, "key", &dst, "a key")
	cmd.SetArgs([]string{"--key-from", "nosuchscheme:x"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	err := Resolve(context.Background(), b)
	if err == nil || dst != "" {
		t.Fatalf("got %q, %v", dst, err)
	}
	// The refusal must be Open's own, not an empty secret read from nothing.
	if errors.Is(err, errEmpty) {
		t.Fatalf("the open error was swallowed: %v", err)
	}
}

func TestDefaultOpenReadsAFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secretsource file: refuses a temp file whose inherited DACL grants a non-owner; that refusal is secretsource's own tested behaviour")
	}
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dst string
	cmd := cmdFor()
	b := AddFrom(cmd, "key", &dst, "a key")
	cmd.SetArgs([]string{"--key-from", "file:" + p})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(context.Background(), b); err != nil || dst != "from-file" {
		t.Fatalf("got %q, %v", dst, err)
	}
}

func TestAddFromListKeepsOrderAndFailsOnBadRef(t *testing.T) {
	fakeOpen(t, map[string]string{"r:1": "one", "r:2": "two"})
	var dst []string
	cmd := cmdFor()
	b := AddFromList(cmd, "old-key", &dst, "old keys")
	cmd.SetArgs([]string{"--old-key-from", "r:1", "--old-key-from", "r:2"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(context.Background(), b); err != nil || len(dst) != 2 || dst[0] != "one" || dst[1] != "two" {
		t.Fatalf("got %v, %v", dst, err)
	}

	var bad []string
	cmd = cmdFor()
	b = AddFromList(cmd, "old-key", &bad, "old keys")
	cmd.SetArgs([]string{"--old-key-from", "r:1", "--old-key-from", "r:none"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(context.Background(), b); err == nil {
		t.Fatal("an unreadable reference was accepted")
	}
}
