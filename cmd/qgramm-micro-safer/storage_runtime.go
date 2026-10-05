//go:build qg_ai_endpoint && qg_e2ee && qg_ai_storage

package main

import "github.com/mgg789/QGramm/internal/microsafer"

func setupStorageRuntime(r *microsafer.Runtime, c microsafer.Config) (func(), error) {
	if !c.Storage.Enabled {
		return func() {}, nil
	}
	h, err := microsafer.OpenStorage(c)
	if err != nil {
		return nil, err
	}
	if err = microsafer.AttachStorageHandler(r, h); err != nil {
		_ = h.Close()
		return nil, err
	}
	return func() { _ = h.Close() }, nil
}
