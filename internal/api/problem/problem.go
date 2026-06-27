// Package problem implements RFC 9457 Problem Details for HTTP APIs.
package problem

import (
	"encoding/json"
	"net/http"
)

const contentType = "application/problem+json"

// Problem is an RFC 9457 problem detail object.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`

	// Extension members (arbitrary key/value pairs)
	Extensions map[string]any `json:"-"`
}

// Write serialises the Problem as application/problem+json and writes it to w.
func Write(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(p.Status)

	// Merge base fields and extension members into one JSON object.
	out := map[string]any{
		"type":   p.Type,
		"title":  p.Title,
		"status": p.Status,
	}
	if p.Detail != "" {
		out["detail"] = p.Detail
	}
	if p.Instance != "" {
		out["instance"] = p.Instance
	}
	for k, v := range p.Extensions {
		out[k] = v
	}
	_ = json.NewEncoder(w).Encode(out)
}

// ── Canonical problem types (stable URIs) ─────────────────────────────────────

const base = "https://vaultnuban.systynlabs.com/problems/"

func Unauthorized(w http.ResponseWriter, detail string) {
	Write(w, Problem{
		Type:   base + "unauthorized",
		Title:  "Unauthorized",
		Status: http.StatusUnauthorized,
		Detail: detail,
	})
}

func NotFound(w http.ResponseWriter, detail string) {
	Write(w, Problem{
		Type:   base + "not-found",
		Title:  "Not Found",
		Status: http.StatusNotFound,
		Detail: detail,
	})
}

func Conflict(w http.ResponseWriter, detail string) {
	Write(w, Problem{
		Type:   base + "conflict",
		Title:  "Conflict",
		Status: http.StatusConflict,
		Detail: detail,
	})
}

func UnprocessableEntity(w http.ResponseWriter, problemType, detail string) {
	Write(w, Problem{
		Type:   base + problemType,
		Title:  "Unprocessable Entity",
		Status: http.StatusUnprocessableEntity,
		Detail: detail,
	})
}

func BadRequest(w http.ResponseWriter, detail string) {
	Write(w, Problem{
		Type:   base + "bad-request",
		Title:  "Bad Request",
		Status: http.StatusBadRequest,
		Detail: detail,
	})
}

func InternalServerError(w http.ResponseWriter, detail string) {
	Write(w, Problem{
		Type:   base + "internal-server-error",
		Title:  "Internal Server Error",
		Status: http.StatusInternalServerError,
		Detail: detail,
	})
}
