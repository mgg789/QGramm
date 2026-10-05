//go:build qg_ai_endpoint && qg_e2ee && qg_ai_storage

package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/mgg789/QGramm/internal/microsafer"
)

func storageCommand(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	configPath := fs.String("config", "qgramm.toml", "standalone endpoint TOML")
	input := fs.String("input", "", "offline JSON resource bundle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("-input is required")
	}
	c, err := microsafer.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	h, err := microsafer.OpenStorage(c)
	if err != nil {
		return err
	}
	defer h.Close()
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	return h.Ingest(context.Background(), raw)
}
