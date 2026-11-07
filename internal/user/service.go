package user

import (
	"context"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
)

type Service struct {
	store     *Store
	tokens    *auth.TokenManager
	dummyHash string // compared against for unknown emails to equalise login timing
}

func NewService(store *Store, tokens *auth.TokenManager) *Service {
	h, _ := auth.HashPassword("timing-equaliser")
	return &Service{store: store, tokens: tokens, dummyHash: h}
}

type SignupInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (in *SignupInput) Validate() error {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	if a, err := mail.ParseAddress(in.Email); err != nil || a.Address != in.Email {
		return apperr.Invalid("a valid email is required")
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		return apperr.Invalid("name must be 1-100 characters")
	}
	// bcrypt only uses the first 72 bytes.
	if len(in.Password) < 8 || len(in.Password) > 72 {
		return apperr.Invalid("password must be 8-72 characters")
	}
	return nil
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (in *LoginInput) Validate() error {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email == "" || in.Password == "" {
		return apperr.Invalid("email and password are required")
	}
	return nil
}

type Session struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        User      `json:"user"`
}

func (s *Service) Signup(ctx context.Context, in SignupInput) (User, error) {
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	return s.store.Create(ctx, in.Email, in.Name, hash, auth.RoleUser)
}

func (s *Service) Login(ctx context.Context, in LoginInput) (Session, error) {
	u, hash, err := s.store.GetByEmail(ctx, in.Email)
	if apperr.Is(err, apperr.KindNotFound) {
		auth.CheckPassword(s.dummyHash, in.Password)
		return Session{}, apperr.Unauthorized("invalid email or password")
	}
	if err != nil {
		return Session{}, err
	}
	if !auth.CheckPassword(hash, in.Password) {
		return Session{}, apperr.Unauthorized("invalid email or password")
	}
	tok, exp, err := s.tokens.Issue(auth.Principal{UserID: u.ID, Role: u.Role})
	if err != nil {
		return Session{}, err
	}
	return Session{AccessToken: tok, TokenType: "Bearer", ExpiresAt: exp, User: u}, nil
}

// SetRole lets an admin promote/demote users. Admins cannot demote themselves, which
// guarantees the system never loses its last admin by accident.
func (s *Service) SetRole(ctx context.Context, actor auth.Principal, id int64, role auth.Role) (User, error) {
	if actor.UserID == id && role != auth.RoleAdmin {
		return User{}, apperr.Invalid("admins cannot demote themselves")
	}
	return s.store.SetRole(ctx, id, role)
}

// EnsureAdmin seeds the bootstrap admin account from configuration (idempotent).
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) error {
	if email == "" {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))
	u, _, err := s.store.GetByEmail(ctx, email)
	switch {
	case err == nil:
		if u.Role != auth.RoleAdmin {
			_, err = s.store.SetRole(ctx, u.ID, auth.RoleAdmin)
		}
		return err
	case apperr.Is(err, apperr.KindNotFound):
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := s.store.Create(ctx, email, "Administrator", hash, auth.RoleAdmin); err != nil {
			return err
		}
		slog.Info("seeded admin account", "email", email)
		return nil
	default:
		return err
	}
}
