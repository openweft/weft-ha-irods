//go:build unix

package keyinstall

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testNeg  = "0123456789abcdef0123456789abcdef"
	testZone = "zone_KEY_0123"
)

func goodKeys() Keys { return Keys{Negotiation: testNeg, Zone: testZone} }

func me(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Uid
}

// setup makes a private dir holding a server_config.json with the given body.
func setup(t *testing.T, body string, mode os.FileMode) (dir, path string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "irods")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func readObj(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{testNeg, testZone, "SECRET-OLD-VALUE"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestInstallMergesAndPreservesFields(t *testing.T) {
	_, path := setup(t, `{"zone_name":"weftZone","zone_port":1247,"nested":{"a":[1,2,{"b":"<&>"}]},"negotiation_key":"SECRET-OLD-VALUE"}`, 0o600)
	changed, err := Install(path, me(t), goodKeys())
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v", changed, err)
	}
	m := readObj(t, path)
	if m["negotiation_key"] != testNeg || m["zone_key"] != testZone {
		t.Fatalf("keys not installed: %v", m)
	}
	if m["zone_name"] != "weftZone" || m["zone_port"] != float64(1247) {
		t.Fatalf("fields lost: %v", m)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"<&>"`) {
		t.Fatalf("value re-escaped: %s", raw)
	}
	if strings.Contains(string(raw), "SECRET-OLD-VALUE") {
		t.Fatal("old key survived")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if got := fileUID(fi); got != uint32(os.Getuid()) {
		t.Fatalf("owner = %d", got)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	_, path := setup(t, `{"zone_name":"z"}`, 0o600)
	if _, err := Install(path, me(t), goodKeys()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	changed, err := Install(path, me(t), goodKeys())
	if err != nil || changed {
		t.Fatalf("second Install = %v, %v", changed, err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("an unchanged file was replaced")
	}
}

func TestInstallRotation(t *testing.T) {
	_, path := setup(t, `{}`, 0o600)
	if _, err := Install(path, me(t), goodKeys()); err != nil {
		t.Fatal(err)
	}
	k := Keys{Negotiation: strings.Repeat("N", 32), Zone: "rotated"}
	changed, err := Install(path, me(t), k)
	if err != nil || !changed {
		t.Fatalf("rotation = %v, %v", changed, err)
	}
	if m := readObj(t, path); m["negotiation_key"] != k.Negotiation || m["zone_key"] != "rotated" {
		t.Fatalf("not rotated: %v", m)
	}
}

func TestInstallRepairsMode(t *testing.T) {
	_, path := setup(t, `{}`, 0o600)
	if _, err := Install(path, me(t), goodKeys()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	changed, err := Install(path, me(t), goodKeys())
	if err != nil || changed {
		t.Fatalf("Install = %v, %v", changed, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode not repaired: %v", fi.Mode().Perm())
	}
}

func TestInstallRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		owner func(t *testing.T) string
		euid  int // -1 keeps the real one
		want  string
	}{
		{"group-writable file", func(t *testing.T) string {
			_, p := setup(t, `{}`, 0o600)
			_ = os.Chmod(p, 0o620)
			return p
		}, me, -1, "writable"},
		{"world-writable file", func(t *testing.T) string {
			_, p := setup(t, `{}`, 0o600)
			_ = os.Chmod(p, 0o602)
			return p
		}, me, -1, "writable"},
		{"group-writable dir", func(t *testing.T) string {
			d, p := setup(t, `{}`, 0o600)
			_ = os.Chmod(d, 0o770)
			return p
		}, me, -1, "writable"},
		{"symlink", func(t *testing.T) string {
			d, p := setup(t, `{}`, 0o600)
			l := filepath.Join(d, "link.json")
			if err := os.Symlink(p, l); err != nil {
				t.Fatal(err)
			}
			return l
		}, me, -1, "symlink"},
		{"missing file", func(t *testing.T) string {
			d, _ := setup(t, `{}`, 0o600)
			return filepath.Join(d, "nope.json")
		}, me, -1, "stat"},
		{"owned by someone else", func(t *testing.T) string {
			_, p := setup(t, `{}`, 0o600)
			return p
		}, func(*testing.T) string { return "4000001" }, 4000002, "owned by uid"},
		{"missing dir", func(t *testing.T) string {
			d, _ := setup(t, `{}`, 0o600)
			return filepath.Join(d, "gone", "server_config.json")
		}, me, -1, "stat"},
		{"directory instead of file", func(t *testing.T) string {
			d, _ := setup(t, `{}`, 0o600)
			p := filepath.Join(d, "sub")
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		}, me, -1, "not a regular file"},
		{"not JSON", func(t *testing.T) string {
			_, p := setup(t, `{"a": SECRET-OLD-VALUE`, 0o600)
			return p
		}, me, -1, "not valid JSON"},
		{"not an object", func(t *testing.T) string {
			_, p := setup(t, `[1]`, 0o600)
			return p
		}, me, -1, "not a JSON object"},
		{"null", func(t *testing.T) string {
			_, p := setup(t, `null`, 0o600)
			return p
		}, me, -1, "not a JSON object"},
		{"too large", func(t *testing.T) string {
			_, p := setup(t, `{"x":"`+strings.Repeat("a", maxConfigSize)+`"}`, 0o600)
			return p
		}, me, -1, "larger than"},
		{"unknown owner", func(t *testing.T) string {
			_, p := setup(t, `{}`, 0o600)
			return p
		}, func(*testing.T) string { return "no-such-account-xyz" }, -1, "owner"},
		{"empty owner", func(t *testing.T) string {
			_, p := setup(t, `{}`, 0o600)
			return p
		}, func(*testing.T) string { return "" }, -1, "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.euid >= 0 {
				old := geteuid
				geteuid = func() int { return tc.euid }
				t.Cleanup(func() { geteuid = old })
			}
			path := tc.setup(t)
			var before []byte
			if b, err := os.ReadFile(path); err == nil {
				before = b
			}
			changed, err := Install(path, tc.owner(t), goodKeys())
			if err == nil || changed {
				t.Fatalf("Install = %v, %v; want a refusal", changed, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", err, tc.want)
			}
			noSecret(t, err)
			if after, rerr := os.ReadFile(path); rerr == nil && string(after) != string(before) {
				t.Fatal("a refused Install changed the file")
			}
		})
	}
}

func TestInstallRefusesInvalidKeysWithoutEchoing(t *testing.T) {
	_, path := setup(t, `{}`, 0o600)
	for _, k := range []Keys{
		{Negotiation: "short", Zone: testZone},
		{Negotiation: strings.Repeat("é", 16), Zone: testZone},
		{Negotiation: testNeg, Zone: ""},
		{Negotiation: testNeg, Zone: strings.Repeat("z", 50)},
		{Negotiation: testNeg, Zone: "has space"},
	} {
		if _, err := Install(path, me(t), k); err == nil {
			t.Fatalf("accepted %+v", k)
		} else if strings.Contains(err.Error(), k.Zone) && k.Zone != "" || strings.Contains(err.Error(), k.Negotiation) {
			t.Fatalf("error echoes a key: %v", err)
		}
	}
}

// A failure after the temp file exists must leave the original intact and no
// temp file behind.
func TestInstallAtomicOnFailure(t *testing.T) {
	dir, path := setup(t, `{"zone_name":"keep"}`, 0o600)
	old := fchown
	fchown = func(*os.File, int, int) error { return errors.New("boom") }
	t.Cleanup(func() { fchown = old })
	changed, err := Install(path, me(t), goodKeys())
	if err == nil || changed {
		t.Fatalf("Install = %v, %v", changed, err)
	}
	noSecret(t, err)
	if b, _ := os.ReadFile(path); string(b) != `{"zone_name":"keep"}` {
		t.Fatalf("original altered: %s", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestInstallChownToOtherAccountFailsCleanlyWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown")
	}
	dir, path := setup(t, `{}`, 0o600)
	// A uid with no passwd entry: the group is left alone, chown is refused.
	if _, err := Install(path, "4000001", goodKeys()); err == nil {
		t.Fatal("chown to another uid should fail without root")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestInstallUnreadableFileAndErrorShape(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	_, path := setup(t, `{}`, 0o600)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	_, err := Install(path, me(t), goodKeys())
	want := "read " + path + ": permission denied"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestInstallFailureSteps(t *testing.T) {
	for _, what := range []string{"chown", "write"} {
		_, path := setup(t, `{}`, 0o600)
		if what == "chown" {
			old := fchown
			fchown = func(*os.File, int, int) error { return errors.New("denied") }
			t.Cleanup(func() { fchown = old })
		}
		if what == "write" {
			// A directory that vanishes after the checks makes the rename fail.
			old := fchown
			fchown = func(f *os.File, _, _ int) error {
				return os.RemoveAll(filepath.Dir(f.Name()))
			}
			t.Cleanup(func() { fchown = old })
		}
		_, err := Install(path, me(t), goodKeys())
		if err == nil {
			t.Fatalf("%s: want an error", what)
		}
		noSecret(t, err)
	}
}

func TestInstallTempDirUnwritable(t *testing.T) {
	dir, path := setup(t, `{}`, 0o600)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	if _, err := Install(path, me(t), goodKeys()); err == nil {
		t.Fatal("want an error")
	}
}

func TestResolveOwner(t *testing.T) {
	u, _ := user.Current()
	for _, o := range []string{u.Username, u.Uid} {
		uid, _, err := resolveOwner(o)
		if err != nil || uid != os.Getuid() {
			t.Fatalf("resolveOwner(%q) = %d, %v", o, uid, err)
		}
	}
	if uid, gid, err := resolveOwner("4000001"); err != nil || uid != 4000001 || gid != -1 {
		t.Fatalf("numeric without passwd entry = %d %d %v", uid, gid, err)
	}
}
