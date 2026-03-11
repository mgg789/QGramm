package httpapi

import (
    "encoding/json"
    "errors"
    "net/http"

    "qgramm/backend/internal/services"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(value)
}

func readJSON(r *http.Request, dst any) error {
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields()
    return dec.Decode(dst)
}

func handleError(w http.ResponseWriter, err error) {
    if err == nil {
        return
    }

    status := http.StatusInternalServerError
    switch {
    case errors.Is(err, services.ErrBadRequest):
        status = http.StatusBadRequest
    case errors.Is(err, services.ErrUnauthorized):
        status = http.StatusUnauthorized
    case errors.Is(err, services.ErrForbidden):
        status = http.StatusForbidden
    case errors.Is(err, services.ErrNotFound):
        status = http.StatusNotFound
    }

    writeJSON(w, status, map[string]any{"error": err.Error()})
}
