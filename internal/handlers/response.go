// internal/handlers/response.go
package handlers

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is the envelope every handler uses for non-2xx responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// respondWithJSON writes payload as a JSON body with the given status code.
func respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// respondWithError writes an ErrorResponse with the given status code.
func respondWithError(w http.ResponseWriter, status int, code, message string) {
	respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

// decodeJSONBody decodes the request body into dst. On failure it writes a
// 400 INVALID_REQUEST response and reports false, so callers can just return.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return false
	}
	return true
}
