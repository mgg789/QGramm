package db

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"

    "github.com/jackc/pgx/v5/pgxpool"
)

func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return fmt.Errorf("read migrations dir: %w", err)
    }

    files := make([]string, 0, len(entries))
    for _, entry := range entries {
        if entry.IsDir() {
            continue
        }
        name := entry.Name()
        if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
            continue
        }
        if strings.HasSuffix(name, ".sql") {
            files = append(files, entry.Name())
        }
    }

    sort.Strings(files)

    for _, file := range files {
        path := filepath.Join(dir, file)
        data, readErr := os.ReadFile(path)
        if readErr != nil {
            return fmt.Errorf("read migration %s: %w", file, readErr)
        }

        if _, execErr := pool.Exec(ctx, string(data)); execErr != nil {
            return fmt.Errorf("execute migration %s: %w", file, execErr)
        }
    }

    return nil
}
