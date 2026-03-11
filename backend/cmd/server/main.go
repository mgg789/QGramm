package main

import (
    "context"
    "fmt"
    "log"
    "math/rand"
    "net/http"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"
    "time"

    "qgramm/backend/internal/auth"
    "qgramm/backend/internal/config"
    "qgramm/backend/internal/db"
    "qgramm/backend/internal/httpapi"
    "qgramm/backend/internal/realtime"
    "qgramm/backend/internal/services"
    "qgramm/backend/internal/storage"
)

func main() {
    rand.Seed(time.Now().UnixNano())

    cfg, err := config.Load()
    if err != nil {
        log.Fatalf("load config: %v", err)
    }

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    pool, err := db.NewPool(ctx, cfg.Database.DSN)
    if err != nil {
        log.Fatalf("database: %v", err)
    }
    defer pool.Close()

    migrationsDir := filepath.Join(".", "migrations")
    if err := db.RunMigrations(ctx, pool, migrationsDir); err != nil {
        log.Fatalf("migrations: %v", err)
    }

    fileStore := storage.NewFileStore(cfg.Storage)
    if err := fileStore.EnsureDirs(); err != nil {
        log.Fatalf("storage dirs: %v", err)
    }

    jwtManager := auth.NewManager(cfg.Security.JWTSecret, cfg.Security.JWTIssuer, cfg.Security.AccessTokenTTL)
    hub := realtime.NewHub()
    svc := services.New(pool, cfg, jwtManager, hub, fileStore)

    if err := svc.BootstrapSystemAccounts(ctx); err != nil {
        log.Fatalf("bootstrap: %v", err)
    }

    router := httpapi.NewRouter(svc)

    address := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
    server := &http.Server{
        Addr:              address,
        Handler:           router,
        ReadHeaderTimeout: 10 * time.Second,
        ReadTimeout:       60 * time.Second,
        WriteTimeout:      60 * time.Second,
        IdleTimeout:       90 * time.Second,
    }

    go func() {
        log.Printf("qgramm backend listening on %s", address)
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("http server: %v", err)
        }
    }()

    <-ctx.Done()
    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer shutdownCancel()

    if err := server.Shutdown(shutdownCtx); err != nil {
        log.Printf("graceful shutdown failed: %v", err)
    }
}
