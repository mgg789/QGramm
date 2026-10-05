//go:build qg_ai_endpoint && qg_e2ee && !qg_ai_storage

package main

import "errors"

func storageCommand([]string) error {
	return errors.New("storage ingestion requires qg_ai_storage build feature")
}
