//go:build qg_ai_endpoint && qg_e2ee

// qgramm-micro-safer is intentionally a separate binary.  It reads its own
// qgramm.toml endpoint deployment and never reuses the server's core config.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mgg789/QGramm/internal/microsafer"
)

// Set by qgramm-build with -ldflags -X main.compiledFeatures. Keeping the
// default is truthful for a direct tagged build. qgramm-build injects the
// feature names from the core build manifest with -ldflags.
var compiledFeatures = "qg_ai_endpoint,qg_e2ee"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = initCommand(os.Args[2:])
	case "join":
		err = joinCommand(os.Args[2:])
	case "run":
		err = runCommand(os.Args[2:])
	case "ingest":
		err = storageCommand(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "qgramm-micro-safer:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: qgramm-micro-safer {init|join|run|ingest} -config qgramm.toml [-chat id]")
}
func load(args []string) (microsafer.Config, string, error) {
	fs := flag.NewFlagSet("micro-safer", flag.ContinueOnError)
	path := fs.String("config", "qgramm.toml", "standalone endpoint TOML")
	chat := fs.String("chat", "", "chat name")
	if err := fs.Parse(args); err != nil {
		return microsafer.Config{}, "", err
	}
	c, err := microsafer.LoadConfig(*path)
	if err == nil && c.Storage.Enabled && !compiledFeature("ai_storage") {
		err = errors.New("storage is configured but this binary lacks qg_ai_storage")
	}
	return c, *chat, err
}

func compiledFeature(name string) bool {
	for _, value := range strings.Split(compiledFeatures, ",") {
		value = strings.TrimSpace(value)
		if value == name || value == "qg_"+name {
			return true
		}
	}
	return false
}
func initCommand(args []string) error {
	c, chat, err := load(args)
	if err != nil {
		return err
	}
	out, err := microsafer.Bootstrap(context.Background(), c, chat)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
func joinCommand(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	path := fs.String("config", "qgramm.toml", "standalone endpoint TOML")
	chat := fs.String("chat", "", "chat name")
	welcomePath := fs.String("welcome", "", "Welcome file or base64")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *welcomePath == "" {
		return errors.New("-welcome is required")
	}
	c, err := microsafer.LoadConfig(*path)
	if err != nil {
		return err
	}
	wire, err := os.ReadFile(*welcomePath)
	if err != nil {
		wire = []byte(strings.TrimSpace(*welcomePath))
	}
	if b, e := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(wire))); e == nil {
		wire = b
	}
	out, err := microsafer.JoinWelcome(context.Background(), c, *chat, wire)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func runCommand(args []string) error {
	c, chat, err := load(args)
	if err != nil {
		return err
	}
	r, err := microsafer.OpenRuntime(context.Background(), c, chat)
	if err != nil {
		return err
	}
	defer r.Participant().Store.Close()
	cleanup, err := setupStorageRuntime(r, c)
	if err != nil {
		return err
	}
	defer cleanup()
	if err = r.RegisterConfiguredHandlers(); err != nil {
		return err
	}
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = r.Run(runCtx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
