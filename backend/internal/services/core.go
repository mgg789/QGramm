package services

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "log"
    "math/rand"
    "strings"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    "qgramm/backend/internal/auth"
    "qgramm/backend/internal/config"
    "qgramm/backend/internal/realtime"
    "qgramm/backend/internal/storage"
)

var (
    ErrUnauthorized = errors.New("unauthorized")
    ErrForbidden    = errors.New("forbidden")
    ErrNotFound     = errors.New("not found")
    ErrBadRequest   = errors.New("bad request")
)

type Service struct {
    pool      *pgxpool.Pool
    cfg       config.Config
    jwt       *auth.Manager
    hub       *realtime.Hub
    fileStore *storage.FileStore
}

func New(pool *pgxpool.Pool, cfg config.Config, jwt *auth.Manager, hub *realtime.Hub, fileStore *storage.FileStore) *Service {
    return &Service{
        pool:      pool,
        cfg:       cfg,
        jwt:       jwt,
        hub:       hub,
        fileStore: fileStore,
    }
}

func (s *Service) JWT() *auth.Manager {
    return s.jwt
}

func (s *Service) Hub() *realtime.Hub {
    return s.hub
}

func (s *Service) BootstrapSystemAccounts(ctx context.Context) error {
    tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return err
    }
    defer func() {
        if err != nil {
            _ = tx.Rollback(ctx)
        }
    }()

    rootID, err := s.ensureSystemUser(ctx, tx, ensureUserInput{
        UID:       s.cfg.Root.RootUID,
        Email:     s.cfg.Root.RootEmail,
        Nickname:  s.cfg.Root.RootNickname,
        FirstName: s.cfg.Root.RootFirstName,
        LastName:  s.cfg.Root.RootLastName,
        IsRoot:    true,
        Trust:     5,
    })
    if err != nil {
        return err
    }

    qgrammID, err := s.ensureSystemUser(ctx, tx, ensureUserInput{
        UID:       s.cfg.Root.QGrammServiceUID,
        Email:     s.cfg.Root.QGrammServiceEmail,
        Nickname:  s.cfg.Root.QGrammServiceNickname,
        FirstName: s.cfg.Root.QGrammServiceFirstName,
        LastName:  s.cfg.Root.QGrammServiceLastName,
        IsRoot:    false,
        Trust:     5,
    })
    if err != nil {
        return err
    }

    if _, execErr := tx.Exec(ctx,
        `INSERT INTO conversations (id, kind, title, is_qgramm, created_by_user_id, metadata)
         VALUES ($1, 'system', 'QGramm', true, $2, '{}'::jsonb)
         ON CONFLICT DO NOTHING`,
        uuid.MustParse("11111111-1111-1111-1111-111111111111"), rootID,
    ); execErr != nil {
        return execErr
    }

    if _, execErr := tx.Exec(ctx,
        `INSERT INTO conversation_members (conversation_id, user_id, role)
         VALUES ($1, $2, 'owner')
         ON CONFLICT DO NOTHING`,
        uuid.MustParse("11111111-1111-1111-1111-111111111111"), qgrammID,
    ); execErr != nil {
        return execErr
    }

    err = tx.Commit(ctx)
    if err != nil {
        return err
    }

    return nil
}

func (s *Service) ensureSystemUser(ctx context.Context, tx pgx.Tx, in ensureUserInput) (uuid.UUID, error) {
    var id uuid.UUID
    err := tx.QueryRow(ctx,
        `SELECT id FROM users WHERE email = $1`,
        in.Email,
    ).Scan(&id)
    if err == nil {
        _, upErr := tx.Exec(ctx,
            `UPDATE users
             SET nickname = $2, first_name = $3, last_name = $4, is_root = $5, trust_level = $6, updated_at = NOW()
             WHERE id = $1`,
            id, in.Nickname, in.FirstName, in.LastName, in.IsRoot, in.Trust,
        )
        return id, upErr
    }

    if !errors.Is(err, pgx.ErrNoRows) {
        return uuid.Nil, err
    }

    id = uuid.New()
    _, err = tx.Exec(ctx,
        `INSERT INTO users (id, uid, email, first_name, last_name, nickname, trust_level, is_root)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
        id, in.UID, strings.ToLower(in.Email), in.FirstName, in.LastName, in.Nickname, in.Trust, in.IsRoot,
    )
    if err != nil {
        return uuid.Nil, err
    }

    return id, nil
}

type ensureUserInput struct {
    UID       string
    Email     string
    FirstName string
    LastName  string
    Nickname  string
    IsRoot    bool
    Trust     int
}

func hashString(value string) string {
    sum := sha256.Sum256([]byte(value))
    return hex.EncodeToString(sum[:])
}

func hashCode(code, pepper string) string {
    return hashString(code + ":" + pepper)
}

func randomDigits6() string {
    return fmt.Sprintf("%06d", rand.Intn(1_000_000))
}

func randomInviteCode() string {
    alphabet := []rune("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")
    part := func(n int) string {
        b := make([]rune, n)
        for i := 0; i < n; i++ {
            b[i] = alphabet[rand.Intn(len(alphabet))]
        }
        return string(b)
    }
    return fmt.Sprintf("%s-%s-%s", part(4), part(4), part(4))
}

func normalizeNickname(n string) string {
    cleaned := strings.ToLower(strings.TrimSpace(n))
    if cleaned == "" {
        return ""
    }
    if !strings.HasPrefix(cleaned, "@") {
        cleaned = "@" + cleaned
    }
    return cleaned
}

func normalizeEmail(email string) string {
    return strings.ToLower(strings.TrimSpace(email))
}

func makeUserUID() string {
    now := time.Now().UTC().Format("060102")
    token := strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
    return fmt.Sprintf("QG-%s-%s", now, token)
}

func parseJSONMap(raw []byte) map[string]any {
    if len(raw) == 0 {
        return map[string]any{}
    }
    out := map[string]any{}
    if err := json.Unmarshal(raw, &out); err != nil {
        log.Printf("parse json map failed: %v", err)
        return map[string]any{}
    }
    return out
}
