//go:build windows

package contextrepo

import "os"

// Windows deployments use the service-level single-instance boundary. The
// file remains private and the API stays portable for local test builds.
func lockFile(_ *os.File) error   { return nil }
func unlockFile(_ *os.File) error { return nil }
