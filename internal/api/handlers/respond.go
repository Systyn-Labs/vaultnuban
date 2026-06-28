package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/logger"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// serverErr logs the real error then writes a generic 500 to the client.
// Use this instead of calling problem.InternalServerError directly when an
// error value is available, so the cause is always visible in logs.
func serverErr(w http.ResponseWriter, r *http.Request, context string, err error) {
	logger.Errorf(context, "%s %s: %v", r.Method, r.URL.Path, err)
	problem.InternalServerError(w, "internal server error")
}
