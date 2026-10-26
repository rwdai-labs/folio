//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package weixin

import (
	"errors"
	"os"
)

func acquireLock(_ *os.File) error {
	return errors.New("weixin: process locking is unsupported on this platform")
}
