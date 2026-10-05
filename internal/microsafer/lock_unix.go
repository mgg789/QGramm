//go:build qg_ai_endpoint && qg_e2ee && (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package microsafer

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type ownerLock struct{ f *os.File }

func acquireOwnerLock(path string) (*ownerLock, error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("microsafer: state is already owned: %w", err)
	}
	return &ownerLock{f: f}, nil
}
func (l *ownerLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	e2 := l.f.Close()
	if err != nil {
		return err
	}
	return e2
}
