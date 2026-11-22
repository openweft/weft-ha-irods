package secretflag

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newCmd(dst *string) (*cobra.Command, *Binding) {
	cmd := &cobra.Command{Use: "x", SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return nil }}
	b := Add(cmd, "token", dst, "a token")
	return cmd, b
}

func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var dst string
	cmd, b := newCmd(&dst)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.Flags().SetOutput(&errb) // pflag prints the deprecation notice here
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		return "", errb.String(), err
	}
	err := Resolve(context.Background(), b)
	return dst, errb.String(), err
}

func TestPlainFlagStillWorksAndWarns(t *testing.T) {
	got, stderr, err := run(t, "--token", "abc")
	if err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !strings.Contains(stderr, "deprecated") || !strings.Contains(stderr, "--token-from") {
		t.Fatalf("no deprecation warning on stderr: %q", stderr)
	}
}

func TestFromFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secretsource file: refuses a temp file whose inherited DACL grants a non-owner; the refusal is secretsource's own tested behaviour")
	}
	p := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, stderr, err := run(t, "--token-from", "file:"+p)
	if err != nil || got != "s3cret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Contains(stderr, "deprecated") {
		t.Fatalf("unexpected warning: %q", stderr)
	}
}

func TestMutuallyExclusive(t *testing.T) {
	if _, _, err := run(t, "--token", "a", "--token-from", "file:/x"); err == nil {
		t.Fatal("both flags accepted")
	}
}

func TestNeitherGiven(t *testing.T) {
	got, _, err := run(t)
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestOpenErrorNamesTheFlagNotTheSecret(t *testing.T) {
	_, _, err := run(t, "--token-from", "file:/nonexistent/zzz")
	if err == nil || !strings.Contains(err.Error(), "--token-from") || strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptySecretRefused(t *testing.T) {
	old := open
	defer func() { open = old }()
	open = func(context.Context, string) ([]byte, error) { return []byte("\n"), nil }
	if _, _, err := run(t, "--token-from", "x:y"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenFailurePropagates(t *testing.T) {
	old := open
	defer func() { open = old }()
	want := errors.New("boom")
	open = func(context.Context, string) ([]byte, error) { return nil, want }
	if _, _, err := run(t, "--token-from", "x:y"); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}
