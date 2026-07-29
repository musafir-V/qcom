// internal/handlers/context.go
package handlers

import (
	"net/http"
)

// Claim values placed on the request context by the auth middleware.
func entityIDFrom(r *http.Request) string {
	id, _ := r.Context().Value("entity_id").(string)
	return id
}

func entityTypeFrom(r *http.Request) string {
	t, _ := r.Context().Value("entity_type").(string)
	return t
}

func phoneFrom(r *http.Request) string {
	p, _ := r.Context().Value("phone").(string)
	return p
}

// requireEntityID returns the caller's entity ID. When it is absent it writes a
// 401 UNAUTHORIZED response carrying message and reports false.
func requireEntityID(w http.ResponseWriter, r *http.Request, message string) (string, bool) {
	id := entityIDFrom(r)
	if id == "" {
		respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", message)
		return "", false
	}
	return id, true
}
