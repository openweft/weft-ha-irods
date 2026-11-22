// Package sealflag registers the two flags that carry the etcd sealing key. The
// shared secretflag package (secretsource) only offers a --name / --name-from
// pair, the plain twin being deprecated; a flag born after that deprecation has
// no plain twin, and the old keys are repeatable, so they live here.
package sealflag

import (
	"context"
	"errors"
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

var errEmpty = errors.New("the secret is empty")

// Binding ties a destination to its --<name>-from flag.
type Binding struct {
	name string
	dst  *string
	list *[]string
	refs []string
	ref  string
}

// AddFrom registers only --name-from and returns the binding to pass to [Resolve].
func AddFrom(cmd *cobra.Command, name string, dst *string, usage string) *Binding {
	b := &Binding{name: name, dst: dst}
	cmd.Flags().StringVar(&b.ref, name+"-from", "", usage+" read from a secret reference, e.g. systemd-creds:"+name)
	return b
}

// AddFromList registers a repeatable --name-from; every reference is resolved,
// in order, into dst.
func AddFromList(cmd *cobra.Command, name string, dst *[]string, usage string) *Binding {
	b := &Binding{name: name, list: dst}
	cmd.Flags().StringArrayVar(&b.refs, name+"-from", nil, usage+" (repeatable) read from a secret reference")
	return b
}

func read(ctx context.Context, name, ref string) (string, error) {
	raw, err := open(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("--%s-from: %w", name, err)
	}
	v := strings.TrimRight(string(raw), "\r\n")
	clear(raw)
	if v == "" {
		return "", fmt.Errorf("--%s-from: %w", name, errEmpty)
	}
	return v, nil
}

// Resolve reads every binding whose flag was given and stores the secret in its
// destination, trimming trailing newlines. A reference that resolves to nothing
// is an error; errors never carry a value.
func Resolve(ctx context.Context, bs ...*Binding) error {
	for _, b := range bs {
		if b.list != nil {
			for _, r := range b.refs {
				v, err := read(ctx, b.name, r)
				if err != nil {
					return err
				}
				*b.list = append(*b.list, v)
			}
			continue
		}
		if b.ref == "" {
			continue
		}
		v, err := read(ctx, b.name, b.ref)
		if err != nil {
			return err
		}
		*b.dst = v
	}
	return nil
}
