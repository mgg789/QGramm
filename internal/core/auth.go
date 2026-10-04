package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"io"
	"net/http"
	"strings"
	"time"
)

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
func Decode(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		Error(w, 400, "invalid or oversized JSON")
		return false
	}
	var trailing any
	if !errors.Is(dec.Decode(&trailing), io.EOF) {
		Error(w, 400, "trailing JSON")
		return false
	}
	return true
}
func (c *Core) deviceActive(r *http.Request, id Identity) bool {
	var active bool
	err := c.reader().QueryRowContext(r.Context(), `SELECT d.revoked=0 AND u.disabled=0 FROM devices d JOIN users u ON u.id=d.user_id WHERE d.id=? AND d.user_id=?`, id.DeviceID, id.UserID).Scan(&active)
	return err == nil && active
}
func (c *Core) authenticate(r *http.Request) (Identity, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) { return ed25519.PublicKey(c.verifyKey), nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(c.Config.Security.Issuer), jwt.WithAudience(c.Config.Security.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid {
		return Identity{}, errors.New("invalid token")
	}
	sub, _ := claims.GetSubject()
	device, _ := claims["device_id"].(string)
	exp, _ := claims.GetExpirationTime()
	iat, _ := claims.GetIssuedAt()
	if sub == "" || device == "" || iat == nil || exp == nil || exp.Sub(iat.Time) > 15*time.Minute {
		return Identity{}, errors.New("short-lived device token required")
	}
	id := Identity{sub, device}
	if !c.deviceActive(r, id) {
		return Identity{}, errors.New("revoked identity")
	}
	return id, nil
}
func (c *Core) authorize(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := c.authenticate(r)
		if err != nil {
			Error(w, 401, "authentication required")
			return
		}
		h(w, r, id)
	}
}
func (c *Core) management(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(secret), []byte(c.managementSecret)) != 1 {
			Error(w, 401, "management authentication required")
			return
		}
		h(w, r)
	}
}
func hashTicket(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func (c *Core) ticket(w http.ResponseWriter, r *http.Request, id Identity) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		Error(w, 500, "entropy unavailable")
		return
	}
	ticket := base64.RawURLEncoding.EncodeToString(b)
	_, err := c.DB.ExecContext(r.Context(), `INSERT INTO tickets(hash,device_id,user_id,expires) VALUES(?,?,?,?)`, hashTicket(ticket), id.DeviceID, id.UserID, time.Now().Add(30*time.Second).Unix())
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	JSON(w, 201, map[string]any{"ticket": ticket, "expires_in": 30})
}
func (c *Core) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, v := range c.Config.Server.Origins {
				if v == origin {
					allowed = true
				}
			}
			if !allowed {
				Error(w, 403, "origin denied")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Chunk-SHA256, Prefer")
			w.Header().Set("Access-Control-Expose-Headers", "Preference-Applied")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
		}
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.TLS == nil && !c.Config.Server.AllowInsecureLoopback && !(c.Config.Server.TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
			Error(w, 400, "HTTPS required")
			return
		}
		if r.URL.Path != "/v1/ws" && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && c.httpSlots != nil {
			select {
			case c.httpSlots <- struct{}{}:
				defer func() { <-c.httpSlots }()
			default:
				w.Header().Set("Retry-After", "1")
				Error(w, 503, "request capacity reached")
				return
			}
		}
		c.Mux.ServeHTTP(w, r)
	})
}
