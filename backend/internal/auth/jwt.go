package auth

import (
    "errors"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "github.com/google/uuid"
)

type Manager struct {
    secret []byte
    issuer string
    ttl    time.Duration
}

type Claims struct {
    UserID    string    `json:"uid"`
    SessionID string    `json:"sid"`
    IsRoot    bool      `json:"root"`
    jwt.RegisteredClaims
}

func NewManager(secret, issuer string, ttl time.Duration) *Manager {
    return &Manager{secret: []byte(secret), issuer: issuer, ttl: ttl}
}

func (m *Manager) Issue(userID uuid.UUID, sessionID uuid.UUID, isRoot bool) (string, time.Time, error) {
    now := time.Now().UTC()
    exp := now.Add(m.ttl)

    claims := Claims{
        UserID:    userID.String(),
        SessionID: sessionID.String(),
        IsRoot:    isRoot,
        RegisteredClaims: jwt.RegisteredClaims{
            Issuer:    m.issuer,
            Subject:   userID.String(),
            IssuedAt:  jwt.NewNumericDate(now),
            ExpiresAt: jwt.NewNumericDate(exp),
            NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
            ID:        uuid.NewString(),
        },
    }

    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    signed, err := token.SignedString(m.secret)
    if err != nil {
        return "", time.Time{}, err
    }

    return signed, exp, nil
}

func (m *Manager) Parse(raw string) (Claims, error) {
    token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
        if token.Method != jwt.SigningMethodHS256 {
            return nil, errors.New("unexpected signing method")
        }
        return m.secret, nil
    })
    if err != nil {
        return Claims{}, err
    }

    claims, ok := token.Claims.(*Claims)
    if !ok || !token.Valid {
        return Claims{}, errors.New("invalid token claims")
    }

    return *claims, nil
}
