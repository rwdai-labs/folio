//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package weixin

import (
	"errors"
	"os"
	"syscall"
)

func acquireLock(f *os.File) error {
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("weixin: channel already in use")
	}
	return nil
}
