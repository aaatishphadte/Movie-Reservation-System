package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustPrincipal is for handlers mounted behind Authenticate; a missing principal is a wiring bug.
func MustPrincipal(ctx context.Context) Principal {
	p, ok := FromContext(ctx)
	if !ok {
		panic("auth: no principal in context; route is missing Authenticate middleware")
	}
	return p
}

func (t *TokenManager) fromRequest(r *http.Request) (p Principal, present bool, err error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Principal{}, false, nil
	}
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return Principal{}, true, apperr.Unauthorized("authorization header must be 'Bearer <token>'")
	}
	p, err = t.Parse(tok)
	return p, true, err
}

// Authenticate requires a valid bearer token.
func Authenticate(t *TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, present, err := t.fromRequest(r)
			if err == nil && !present {
				err = apperr.Unauthorized("missing bearer token")
			}
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// OptionalAuthenticate attaches a principal when a token is sent, but allows anonymous access.
func OptionalAuthenticate(t *TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, present, err := t.fromRequest(r)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			if present {
				r = r.WithContext(WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireRole enforces RBAC; must be mounted after Authenticate.
func RequireRole(role Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok {
				httpx.Error(w, r, apperr.Unauthorized("authentication required"))
				return
			}
			if p.Role != role {
				httpx.Error(w, r, apperr.Forbidden("requires %s role", role))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
