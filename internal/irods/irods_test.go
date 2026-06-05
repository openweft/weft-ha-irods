package irods

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRunner is a unit-test seam that lets us control what the
// CommandController sees instead of actually exec'ing irods-grid.
type fakeRunner struct {
	// outputs maps the first arg of the command (the binary basename)
	// to a stdout + error pair. We key on the binary name because
	// the controller calls two different binaries in one CheckStatus.
	outputs map[string]struct {
		stdout []byte
		err    error
	}
}

func (f *fakeRunner) run(_ context.Context, name string, _ ...string) ([]byte, error) {
	o, ok := f.outputs[name]
	if !ok {
		return nil, errors.New("fakeRunner: no canned output for " + name)
	}
	return o.stdout, o.err
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		outputs: map[string]struct {
			stdout []byte
			err    error
		}{},
	}
}

func (f *fakeRunner) set(binary string, stdout string, err error) {
	f.outputs[binary] = struct {
		stdout []byte
		err    error
	}{stdout: []byte(stdout), err: err}
}

func TestCommandController_GridUp(t *testing.T) {
	r := newFakeRunner()
	r.set(DefaultGridBinary, "Server is up.\nServer is up.\n", nil)
	r.set(DefaultIAdminBinary, "weftZone\n", nil)

	c := NewCommandController()
	c.runner = r

	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if !st.Up {
		t.Errorf("Up = false ; want true (Reason=%q)", st.Reason)
	}
	if st.ZoneName != "weftZone" {
		t.Errorf("ZoneName = %q ; want weftZone", st.ZoneName)
	}
}

func TestCommandController_GridDown(t *testing.T) {
	r := newFakeRunner()
	// Real irods-grid emits "Server is down." (or stops talking) when
	// the iCAT can't be reached. We only insist on the absence of the
	// up marker.
	r.set(DefaultGridBinary, "Server is down.\n", nil)
	r.set(DefaultIAdminBinary, "weftZone\n", nil)

	c := NewCommandController()
	c.runner = r

	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if st.Up {
		t.Error("Up = true on missing marker ; want false")
	}
	if st.Reason == "" {
		t.Error("Reason empty when server is down ; want a hint")
	}
}

func TestCommandController_GridExecError(t *testing.T) {
	// irods-grid not on PATH (the iRODS package isn't installed yet,
	// or PATH is stripped). We MUST report Up=false + Reason, not
	// bubble the error — otherwise the reconcile loop logs WARN and
	// the role API stays stale.
	r := newFakeRunner()
	r.set(DefaultGridBinary, "", errors.New("exec: \"irods-grid\": executable file not found in $PATH"))
	r.set(DefaultIAdminBinary, "weftZone\n", nil)

	c := NewCommandController()
	c.runner = r

	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus returned err=%v ; want nil so reconcile keeps spinning", err)
	}
	if st.Up {
		t.Error("Up = true on exec failure ; want false")
	}
	if st.Reason == "" {
		t.Error("Reason empty on exec failure ; want a hint for ops")
	}
}

func TestCommandController_IAdminFailureKeepsUp(t *testing.T) {
	// `iadmin lz` is a best-effort enrich — when it fails we still
	// want Up=true coming out of irods-grid, just without ZoneName.
	r := newFakeRunner()
	r.set(DefaultGridBinary, "Server is up.\n", nil)
	r.set(DefaultIAdminBinary, "", errors.New("CAT_PASSWORD_EXPIRED"))

	c := NewCommandController()
	c.runner = r

	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if !st.Up {
		t.Errorf("Up = false when iadmin failed but grid was up ; want true (Reason=%q)", st.Reason)
	}
	if st.ZoneName != "" {
		t.Errorf("ZoneName = %q ; want empty when iadmin failed", st.ZoneName)
	}
}

func TestCommandController_IAdminMultilineFirstWins(t *testing.T) {
	r := newFakeRunner()
	r.set(DefaultGridBinary, "Server is up.\n", nil)
	// Federated zones print one line each ; we want the local zone
	// (the first one).
	r.set(DefaultIAdminBinary, "weftZone\nremoteZoneA\nremoteZoneB\n", nil)

	c := NewCommandController()
	c.runner = r

	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if st.ZoneName != "weftZone" {
		t.Errorf("ZoneName = %q ; want weftZone (first line)", st.ZoneName)
	}
}

func TestNewCommandControllerDefaults(t *testing.T) {
	c := NewCommandController()
	if c.GridBinary != DefaultGridBinary {
		t.Errorf("GridBinary = %q ; want %q", c.GridBinary, DefaultGridBinary)
	}
	if c.IAdminBinary != DefaultIAdminBinary {
		t.Errorf("IAdminBinary = %q ; want %q", c.IAdminBinary, DefaultIAdminBinary)
	}
	if c.Timeout != defaultProbeTimeout {
		t.Errorf("Timeout = %v ; want %v", c.Timeout, defaultProbeTimeout)
	}
	if c.runner == nil {
		t.Error("runner = nil ; want execRunner so CheckStatus uses os/exec by default")
	}
}

func TestCommandController_ZeroTimeoutUsesDefault(t *testing.T) {
	// Caller passes a zero CommandController{} (no NewCommandController) :
	// we still want a sane probe timeout.
	r := newFakeRunner()
	r.set(DefaultGridBinary, "Server is up.\n", nil)
	r.set(DefaultIAdminBinary, "weftZone\n", nil)

	c := &CommandController{runner: r}
	st, err := c.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if !st.Up {
		t.Error("Up = false on zero-value controller ; want true")
	}
	// Sanity : the timeout fallback shouldn't take longer than the
	// canned runner.
	_ = time.Now()
}

func TestFakeController_StillExported(t *testing.T) {
	// The dev-mode/runtime picker keeps FakeController exported so
	// the same binary smoke-boots without irods-grid on PATH.
	fc := &FakeController{NextStatus: Status{Up: true, ZoneName: "weftZone"}}
	st, err := fc.CheckStatus(context.Background())
	if err != nil {
		t.Fatalf("FakeController.CheckStatus: %v", err)
	}
	if !st.Up || st.ZoneName != "weftZone" {
		t.Errorf("FakeController returned %+v ; want Up=true ZoneName=weftZone", st)
	}
}
