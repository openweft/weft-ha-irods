// Package secretflag lets a cobra command take a secret by reference.
//
// A secret given as a flag value is visible in `ps` and in
// /proc/<pid>/cmdline. Each secret therefore gets a `--<name>-from <ref>`
// twin resolved through github.com/go-authn/secretsource (systemd-creds:,
// keychain:, keepassxc:, file:). The plain `--<name>` flag stays for one
// minor release, deprecated (cobra prints the warning on stderr), and is
// mutually exclusive with its twin.
package secretflag

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-authn/secretsource"
	"github.com/spf13/cobra"
)

// open is replaced by tests.
var open = func(ctx context.Context, ref string) ([]byte, error) {
	s, err := secretsource.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return append([]byte(nil), s.Bytes()...), nil
}

// Binding ties a secret destination to its --<name>-from twin.
type Binding struct {
	name string
	dst  *string
	ref  string
}

// Add registers --name (deprecated, plain value) and --name-from on cmd and
// returns the binding to pass to [Resolve]. The two flags are mutually
// exclusive.
func Add(cmd *cobra.Command, name string, dst *string, usage string) *Binding {
	b := &Binding{name: name, dst: dst}
	f := cmd.Flags()
	f.StringVar(dst, name, "", usage+" (DEPRECATED: visible in ps; use --"+name+"-from)")
	_ = f.MarkDeprecated(name, "the value is visible in `ps` and /proc/PID/cmdline; use --"+name+"-from <ref> (systemd-creds:, keychain:, keepassxc:, file:)")
	f.StringVar(&b.ref, name+"-from", "", usage+" read from a secret reference, e.g. systemd-creds:"+name)
	cmd.MarkFlagsMutuallyExclusive(name, name+"-from")
	return b
}

// Resolve reads every binding whose --<name>-from was given and stores the
// secret in its destination, trimming one trailing newline. A reference that
// resolves to nothing is an error.
func Resolve(ctx context.Context, bs ...*Binding) error {
	for _, b := range bs {
		if b.ref == "" {
			continue
		}
		raw, err := open(ctx, b.ref)
		if err != nil {
			return fmt.Errorf("--%s-from: %w", b.name, err)
		}
		v := strings.TrimRight(string(raw), "\r\n")
		clear(raw)
		if v == "" {
			return fmt.Errorf("--%s-from: the secret is empty", b.name)
		}
		*b.dst = v
	}
	return nil
}
