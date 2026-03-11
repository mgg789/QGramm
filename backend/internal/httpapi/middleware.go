package httpapi

import (
    "context"
    "net/http"
    "strings"

    "qgramm/backend/internal/services"
)

type contextKey string

const identityKey contextKey = "identity"

func withIdentity(ctx context.Context, identity services.Identity) context.Context {
    return context.WithValue(ctx, identityKey, identity)
}

func identityFromContext(ctx context.Context) (services.Identity, bool) {
    value := ctx.Value(identityKey)
    if value == nil {
        return services.Identity{}, false
    }
    identity, ok := value.(services.Identity)
    return identity, ok
}

func authMiddleware(svc *services.Service) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            token := extractAccessToken(r)
            if token == "" {
                writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing access token"})
                return
            }

            identity, err := svc.AuthenticateAccessToken(r.Context(), token)
            if err != nil {
                handleError(w, err)
                return
            }

            next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
        })
    }
}

func rootMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        identity, ok := identityFromContext(r.Context())
        if !ok || !identity.IsRoot {
            writeJSON(w, http.StatusForbidden, map[string]any{"error": "root access required"})
            return
        }
        next.ServeHTTP(w, r)
    })
}

func extractAccessToken(r *http.Request) string {
    raw := strings.TrimSpace(r.Header.Get("Authorization"))
    if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
        return strings.TrimSpace(raw[7:])
    }

    if query := strings.TrimSpace(r.URL.Query().Get("access_token")); query != "" {
        return query
    }

    return ""
}
