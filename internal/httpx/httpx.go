// Package httpx holds small, shared HTTP helpers: JSON encoding/decoding, error mapping, params.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
)

const maxBodyBytes = 1 << 20

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type validator interface{ Validate() error }

// Decode reads a bounded JSON body, rejects unknown fields and runs Validate() if implemented.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.Invalid("malformed JSON body: %v", err)
	}
	if v, ok := dst.(validator); ok {
		return v.Validate()
	}
	return nil
}

var statusByKind = map[apperr.Kind]int{
	apperr.KindInvalid:      http.StatusBadRequest,
	apperr.KindUnauthorized: http.StatusUnauthorized,
	apperr.KindForbidden:    http.StatusForbidden,
	apperr.KindNotFound:     http.StatusNotFound,
	apperr.KindConflict:     http.StatusConflict,
	apperr.KindRateLimited:  http.StatusTooManyRequests,
}

// Error writes a consistent error envelope. Unknown errors are logged and hidden from clients.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		status, ok := statusByKind[ae.Kind]
		if !ok {
			status = http.StatusInternalServerError
		}
		JSON(w, status, errorBody(string(ae.Kind), ae.Msg))
		return
	}
	slog.ErrorContext(r.Context(), "unhandled error",
		"err", err, "method", r.Method, "path", r.URL.Path,
		"request_id", middleware.GetReqID(r.Context()))
	JSON(w, http.StatusInternalServerError, errorBody("internal", "internal server error"))
}

func errorBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

// PathID parses a positive int64 URL parameter.
func PathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.Invalid("invalid %s", name)
	}
	return id, nil
}

// QueryID parses an optional positive int64 query parameter.
func QueryID(r *http.Request, name string) (*int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return nil, apperr.Invalid("invalid %s", name)
	}
	return &id, nil
}

type Page struct {
	Limit  int
	Offset int
}

// Paging reads ?limit= (1..100, default 20) and ?offset= (>=0).
func Paging(r *http.Request) Page {
	p := Page{Limit: 20}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		p.Limit = min(v, 100)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		p.Offset = v
	}
	return p
}

// List is the standard envelope for collection responses.
type List[T any] struct {
	Items  []T `json:"items"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}
