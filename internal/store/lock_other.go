//go:build !linux

package store

import "os"

// The service image is Linux. Other platforms retain the same API for local
// development; process-level locking is provided by the platform runner.
func lockFile(_ *os.File) error { return nil }

func unlockFile(_ *os.File) error { return nil }
