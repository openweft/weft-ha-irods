// Package irods is the thin shell around the local iRODS server :
// status probe via `irods-grid`, server-config rendering, on-demand
// restart. The reconcile loop talks to this package rather than
// shelling out directly so unit tests can swap a fake.
package irods

import (
	"context"
	"errors"
)

// Status is the result of a local iRODS server health check.
type Status struct {
	// Up is true when the local iRODS server accepts a status ping
	// (irods-grid status returns "Server is up").
	Up bool
	// ZoneName is what the server reports as its serving zone — used
	// to detect misconfiguration (e.g. operator changed zone_name in
	// the plugin input on a zone already bootstrapped under a
	// different name).
	ZoneName string
	// Reason carries the first non-Up status line if Up = false.
	Reason string
}

// Controller is the surface the reconcile loop uses.
type Controller interface {
	// CheckStatus probes the local server and returns the freshest
	// Status snapshot. Should be cheap (a few hundred ms at most) so
	// it can fire every reconcile tick.
	CheckStatus(ctx context.Context) (Status, error)
}

// ErrNotImplemented signals a path that doesn't have an in-process
// fake. Used by the FakeController.RestartIfChanged stub.
var ErrNotImplemented = errors.New("irods: not implemented in scaffold build")

// FakeController is a test double : returns whatever Status the test
// pre-loaded into it. Used in the reconcile-loop unit tests so the
// loop's logic can be exercised without a running irods-server.
type FakeController struct {
	NextStatus Status
	NextErr    error
}

// CheckStatus implements Controller.
func (f *FakeController) CheckStatus(_ context.Context) (Status, error) {
	if f.NextErr != nil {
		return Status{}, f.NextErr
	}
	return f.NextStatus, nil
}
