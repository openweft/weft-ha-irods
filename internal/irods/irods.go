// Package irods is the thin shell around the local iRODS server :
// status probe via `irods-grid`, zone introspection via `iadmin lz`,
// on-demand restart. The reconcile loop talks to this package rather
// than shelling out directly so unit tests can swap a fake.
package irods

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// Default probe timeout. iRODS' irods-grid is a control-plane call
// (not a data-plane round-trip) so 5s is generous ; a frozen server
// falls out of the L4 pool within one reconcile tick.
const defaultProbeTimeout = 5 * time.Second

// Binary paths the iRODS server packages install to. The agent runs
// in the same micro-VM as iRODS so these are the real paths inside
// the container, not the host's.
const (
	DefaultGridBinary   = "irods-grid"
	DefaultIAdminBinary = "iadmin"
)

// upMarker is the substring the agent looks for in the irods-grid
// stdout to decide the server is up. iRODS prints one line per server
// in the grid ; we treat any one of them being "up" as up because the
// agent only cares about the LOCAL server (the only one it owns).
const upMarker = "Server is up."

// CommandController is the production Controller : it shells out to
// `irods-grid` + `iadmin lz` against the local iRODS server. The
// agent runs inside the same micro-VM as iRODS so PATH lookup hits
// /usr/bin/irods-grid and /usr/bin/iadmin (the upstream package
// install location).
//
// Failed exec OR a non-Up stdout surface as Up=false + Reason instead
// of bubbling an error up — the reconcile loop's 5-second tick is the
// retry policy and the L4 pool needs a clean drain signal, not a
// background panic.
type CommandController struct {
	// GridBinary is the path to `irods-grid`. Empty falls back to
	// DefaultGridBinary (looked up on PATH).
	GridBinary string
	// IAdminBinary is the path to `iadmin`. Empty falls back to
	// DefaultIAdminBinary (looked up on PATH).
	IAdminBinary string
	// Timeout caps a single exec. Zero falls back to defaultProbeTimeout.
	Timeout time.Duration
	// runner is the seam unit tests inject to avoid actually exec'ing
	// the host. Nil means "use real os/exec".
	runner commandRunner
}

// commandRunner is the seam unit tests inject so CheckStatus can be
// exercised without a real irods-grid on PATH. The default
// implementation is execRunner which delegates to os/exec.
type commandRunner interface {
	run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Surface stderr alongside the error so the agent's log has
		// something operators can grep for ("CAT_PASSWORD_EXPIRED",
		// "RECONNECTION_DENIED", ...).
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, s)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// NewCommandController returns a CommandController with the standard
// binary paths + a 5-second per-command timeout.
func NewCommandController() *CommandController {
	return &CommandController{
		GridBinary:   DefaultGridBinary,
		IAdminBinary: DefaultIAdminBinary,
		Timeout:      defaultProbeTimeout,
		runner:       execRunner{},
	}
}

// CheckStatus probes the local iRODS server. Two commands :
//
//   - `irods-grid status --all` — decides Up. We look for the literal
//     "Server is up." substring in stdout. Any exec error or missing
//     marker flips Up to false.
//   - `iadmin lz` — best-effort enrich of Status.ZoneName. Failure
//     does NOT flip Up — the L4 pool cares about whether the server
//     accepts connections, and the zone name is informational only.
func (c *CommandController) CheckStatus(ctx context.Context) (Status, error) {
	r := c.runner
	if r == nil {
		r = execRunner{}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	grid := c.GridBinary
	if grid == "" {
		grid = DefaultGridBinary
	}
	iadmin := c.IAdminBinary
	if iadmin == "" {
		iadmin = DefaultIAdminBinary
	}

	// 1. Probe Up via irods-grid status --all.
	gridCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, err := r.run(gridCtx, grid, "status", "--all")
	if err != nil {
		return Status{Up: false, Reason: fmt.Sprintf("irods-grid: %s", err.Error())}, nil
	}
	if !bytes.Contains(stdout, []byte(upMarker)) {
		return Status{
			Up:     false,
			Reason: fmt.Sprintf("irods-grid: missing %q in stdout", upMarker),
		}, nil
	}

	st := Status{Up: true}

	// 2. Best-effort zone-name enrich. iadmin lz prints one zone per
	// line ; with no resource federation that's just the local zone.
	adminCtx, cancel2 := context.WithTimeout(ctx, timeout)
	defer cancel2()
	if zone, err := c.parseZone(adminCtx, r, iadmin); err == nil {
		st.ZoneName = zone
	}
	return st, nil
}

// parseZone runs `iadmin lz` and returns the first non-empty zone
// line. iadmin's output is one zone per line ; the local zone is
// the first one printed.
func (c *CommandController) parseZone(ctx context.Context, r commandRunner, iadmin string) (string, error) {
	stdout, err := r.run(ctx, iadmin, "lz")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		return s, nil
	}
	return "", errors.New("iadmin lz: empty output")
}

// compile-time assertion : CommandController implements Controller.
var _ Controller = (*CommandController)(nil)
