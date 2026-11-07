// Package auth provides JWT issuance/verification, password hashing and RBAC middleware.
package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

// Principal is the authenticated caller attached to the request context.
type Principal struct {
	UserID int64
	Role   Role
}

func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

const issuer = "movie-reservation"

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), ttl: ttl}
}

type claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

func (t *TokenManager) Issue(p Principal) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(t.ttl)
	c := claims{
		Role: p.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(p.UserID, 10),
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
	return s, exp, err
}

func (t *TokenManager) Parse(token string) (Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Principal{}, apperr.Unauthorized("token expired")
		}
		return Principal{}, apperr.Unauthorized("invalid token")
	}
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || !c.Role.Valid() {
		return Principal{}, apperr.Unauthorized("invalid token")
	}
	return Principal{UserID: id, Role: c.Role}, nil
}
