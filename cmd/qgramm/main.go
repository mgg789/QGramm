package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	_ "github.com/mgg789/QGramm/internal/modules"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var compiledFeatures string

func main() {
	if err := run(); err != nil {
		log.Printf("qgramm: %s", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "qgramm.toml", "TOML configuration")
	backup := flag.String("backup", "", "write consistent SQLite backup (server must be stopped)")
	check := flag.String("healthcheck", "", "HTTP healthcheck URL")
	flag.Parse()
	if *check != "" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(*check)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("health status %d", resp.StatusCode)
		}
		return nil
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	c, err := core.Open(cfg, strings.FieldsFunc(compiledFeatures, func(r rune) bool { return r == ',' }))
	if err != nil {
		return err
	}
	defer c.Close()
	if *backup != "" {
		return createBackup(c, *backup)
	}
	if cfg.Server.AllowInsecureLoopback {
		host, _, err := net.SplitHostPort(cfg.Server.Listen)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("insecure development listener must be loopback")
		}
	}
	server := &http.Server{Addr: cfg.Server.Listen, Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		if cfg.Server.TLSCert != "" {
			done <- server.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			done <- server.ListenAndServe()
		}
	}()
	log.Printf("QGramm listening on %s; features=%s; max_connections=%d", cfg.Server.Listen, compiledFeatures, cfg.Capacity.MaxConnections)
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
	}
	return err
}
func createBackup(c *core.Core, path string) error {
	// Reserve atomically with restrictive permissions; SQLite accepts an empty file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = c.DB.Exec(`VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Chmod(path, 0600)
}
