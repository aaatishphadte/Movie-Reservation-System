// Package apperr defines transport-agnostic domain errors. Services return these;
// the HTTP layer maps each Kind to a status code. Anything else is treated as internal.
package apperr

import (
	"errors"
	"fmt"
)

type Kind string

const (
	KindInvalid      Kind = "invalid_argument"
	KindUnauthorized Kind = "unauthenticated"
	KindForbidden    Kind = "permission_denied"
	KindNotFound     Kind = "not_found"
	KindConflict     Kind = "conflict"
	KindRateLimited  Kind = "rate_limited"
)

type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Msg }

func newf(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

func Invalid(format string, args ...any) error      { return newf(KindInvalid, format, args...) }
func Unauthorized(format string, args ...any) error { return newf(KindUnauthorized, format, args...) }
func Forbidden(format string, args ...any) error    { return newf(KindForbidden, format, args...) }
func NotFound(format string, args ...any) error     { return newf(KindNotFound, format, args...) }
func Conflict(format string, args ...any) error     { return newf(KindConflict, format, args...) }
func RateLimited(format string, args ...any) error  { return newf(KindRateLimited, format, args...) }

// Is reports whether err is an *Error of the given kind.
func Is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}
