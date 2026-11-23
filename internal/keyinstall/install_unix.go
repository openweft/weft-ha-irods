//go:build unix

package keyinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// maxConfigSize bounds what is read: a real server_config.json is a few KiB.
const maxConfigSize = 1 << 20

// Seams for tests: the effective uid and the ownership change need root to be
// exercised for real.
var (
	geteuid = os.Geteuid
	fchown  = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
)

// Install merges k into the server configuration file at path and reports
// whether the file changed. owner is the iRODS service account (a user name or
// a numeric uid); the file ends up owned by it, mode 0600.
//
// It refuses, writing nothing, when the file or its directory is not what a
// secrets file's home must be: a symlink, group- or world-writable, or owned by
// anyone other than the service account, the agent's own user or root.
func Install(path, owner string, k Keys) (changed bool, err error) {
	uid, gid, err := resolveOwner(owner)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	if _, err := checkNode(dir, true, uid); err != nil {
		return false, err
	}
	fi, err := checkNode(path, false, uid)
	if err != nil {
		return false, err
	}
	if fi.Size() > maxConfigSize {
		return false, fmt.Errorf("%s: larger than %d bytes", path, maxConfigSize)
	}
	doc, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, pathErr(err))
	}
	out, changed, err := merge(doc, k)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		if fi.Mode().Perm() == 0o600 && fileUID(fi) == uint32(uid) {
			return false, nil
		}
		// Right content, wrong mode or owner: rewrite to repair it.
		out = doc
	}
	if err := replace(dir, path, out, uid, gid); err != nil {
		return false, err
	}
	return changed, nil
}

// replace writes data to a temp file beside path and renames it over path. The
// temp file is removed on any failure, so the original is never half-written.
func replace(dir, path string, data []byte, uid, gid int) (err error) {
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, pathErr(err))
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	for _, step := range []struct {
		what string
		do   func() error
	}{
		{"chmod", func() error { return tmp.Chmod(0o600) }},
		{"chown", func() error { return fchown(tmp, uid, gid) }},
		{"write", func() error { _, werr := tmp.Write(data); return werr }},
		{"fsync", tmp.Sync},
		{"close", tmp.Close},
		{"rename", func() error { return os.Rename(tmp.Name(), path) }},
	} {
		if err := step.do(); err != nil {
			return fmt.Errorf("%s of the new %s: %w", step.what, filepath.Base(path), pathErr(err))
		}
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// pathErr drops the *fs.PathError wrapper so an error carries the operation's
// cause only; paths are added by the caller.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func resolveOwner(owner string) (uid, gid int, err error) {
	if owner == "" {
		return 0, 0, errors.New("server config owner must not be empty")
	}
	var u *user.User
	if n, perr := strconv.ParseUint(owner, 10, 32); perr == nil {
		if u, err = user.LookupId(owner); err != nil {
			return int(n), -1, nil // no passwd entry: keep the group as is
		}
	} else if u, err = user.Lookup(owner); err != nil {
		return 0, 0, fmt.Errorf("server config owner %q: %w", owner, err)
	}
	uid, _ = strconv.Atoi(u.Uid)
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		gid = -1 // a non-numeric gid (some platforms): keep the group as is
	}
	return uid, gid, nil
}

// checkNode refuses a path that is a symlink (Lstat), not of the wanted kind,
// group- or world-writable, or owned by anyone but the service account uid,
// this process's user, or root.
func checkNode(path string, wantDir bool, uid int) (fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, pathErr(err))
	}
	switch {
	case wantDir && !fi.IsDir():
		return nil, fmt.Errorf("%s: not a directory", path)
	case !wantDir && !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%s: not a regular file (symlinks are refused)", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s: group- or world-writable (mode %04o)", path, fi.Mode().Perm())
	}
	if o := fileUID(fi); o != uint32(uid) && o != uint32(geteuid()) && o != 0 {
		return nil, fmt.Errorf("%s: owned by uid %d, not the service account, this agent or root", path, o)
	}
	return fi, nil
}

// fileUID is the owner of fi; Lstat on a Unix host always yields a Stat_t.
func fileUID(fi fs.FileInfo) uint32 {
	return fi.Sys().(*syscall.Stat_t).Uid
}
