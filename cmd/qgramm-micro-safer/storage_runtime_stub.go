//go:build qg_ai_endpoint && qg_e2ee && !qg_ai_storage

package main

import (
	"errors"
	"github.com/mgg789/QGramm/internal/microsafer"
)

func setupStorageRuntime(_ *microsafer.Runtime, c microsafer.Config) (func(), error) {
	if !c.Storage.Enabled {
		return func() {}, nil
	}
	return func() {}, errors.New("storage is configured but qg_ai_storage is absent from this binary")
}
