// Package user handles accounts, authentication and role management.
package user

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/postgres"
)

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      auth.Role `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const cols = `id, email::text, name, role::text, created_at`

func scan(row pgx.Row, extra ...any) (User, error) {
	var u User
	var role string
	dest := append([]any{&u.ID, &u.Email, &u.Name, &role, &u.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, apperr.NotFound("user not found")
		}
		return User{}, err
	}
	u.Role = auth.Role(role)
	return u, nil
}

func (s *Store) Create(ctx context.Context, email, name, hash string, role auth.Role) (User, error) {
	u, err := scan(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, role) VALUES ($1, $2, $3, $4::user_role) RETURNING `+cols,
		email, name, hash, string(role)))
	if postgres.IsUniqueViolation(err, "") {
		return User{}, apperr.Conflict("email already registered")
	}
	return u, err
}

// GetByEmail returns the user and its password hash.
func (s *Store) GetByEmail(ctx context.Context, email string) (User, string, error) {
	var hash string
	u, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+`, password_hash FROM users WHERE email = $1`, email), &hash)
	return u, hash, err
}

func (s *Store) GetByID(ctx context.Context, id int64) (User, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM users WHERE id = $1`, id))
}

func (s *Store) SetRole(ctx context.Context, id int64, role auth.Role) (User, error) {
	return scan(s.db.QueryRow(ctx,
		`UPDATE users SET role = $2::user_role WHERE id = $1 RETURNING `+cols, id, string(role)))
}

func (s *Store) List(ctx context.Context, p httpx.Page) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM users ORDER BY id LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
