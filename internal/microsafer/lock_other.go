//go:build qg_ai_endpoint && qg_e2ee && !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package microsafer

import "errors"

type ownerLock struct{}

func acquireOwnerLock(string) (*ownerLock, error) {
	return nil, errors.New("microsafer: OS file locking is unsupported on this platform")
}
func (*ownerLock) Close() error { return nil }
