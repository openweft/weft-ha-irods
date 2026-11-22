package main

import (
	"bytes"
	"strings"
	"testing"
)

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSecretRefFailureStopsBeforeValidation(t *testing.T) {
	_, err := runRoot(t, "agent", "--admin-password-from", "file:/nonexistent/zzz")
	if err == nil || !strings.Contains(err.Error(), "--admin-password-from") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlainAndFromAreExclusive(t *testing.T) {
	for _, n := range []string{"admin-password", "icat-db-password", "negotiation-key", "control-plane-key", "zone-key"} {
		if _, err := runRoot(t, "agent", "--"+n, "x", "--"+n+"-from", "file:/x"); err == nil {
			t.Errorf("--%s and --%s-from accepted together", n, n)
		}
	}
}

func TestValidationStillRuns(t *testing.T) {
	_, err := runRoot(t, "agent")
	if err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("err = %v", err)
	}
}

func TestVersion(t *testing.T) {
	out, err := runRoot(t, "version")
	if err != nil || !strings.Contains(out, "weft-ha-irods") {
		t.Fatalf("%q %v", out, err)
	}
}
