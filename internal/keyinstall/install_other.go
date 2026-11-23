//go:build !unix

package keyinstall

import "errors"

// Install is unsupported off Unix: iRODS servers run on Linux and the
// ownership checks have no meaning elsewhere.
func Install(path, owner string, k Keys) (bool, error) {
	return false, errors.New("installing zone keys needs a Unix host")
}
