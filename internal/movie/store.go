// Package movie manages the movie catalogue and genres.
package movie

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

type Movie struct {
	ID              int64     `json:"id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	PosterURL       string    `json:"poster_url"`
	DurationMinutes int       `json:"duration_minutes"`
	Genres          []string  `json:"genres"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Input struct {
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	PosterURL       string   `json:"poster_url"`
	DurationMinutes int      `json:"duration_minutes"`
	Genres          []string `json:"genres"`
}

func (in *Input) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > 200 {
		return apperr.Invalid("title must be 1-200 characters")
	}
	if utf8.RuneCountInString(in.Description) > 5000 {
		return apperr.Invalid("description must be at most 5000 characters")
	}
	if in.DurationMinutes < 1 || in.DurationMinutes > 600 {
		return apperr.Invalid("duration_minutes must be between 1 and 600")
	}
	if in.PosterURL != "" {
		u, err := url.ParseRequestURI(in.PosterURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return apperr.Invalid("poster_url must be an absolute http(s) URL")
		}
	}
	seen := map[string]bool{}
	genres := make([]string, 0, len(in.Genres))
	for _, g := range in.Genres {
		g = strings.TrimSpace(g)
		if g == "" || len(g) > 50 {
			return apperr.Invalid("genre names must be 1-50 characters")
		}
		if k := strings.ToLower(g); !seen[k] {
			seen[k] = true
			genres = append(genres, g)
		}
	}
	if len(genres) > 10 {
		return apperr.Invalid("at most 10 genres per movie")
	}
	in.Genres = genres
	return nil
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const selectMovie = `
SELECT m.id, m.title, m.description, m.poster_url, m.duration_minutes, m.created_at, m.updated_at,
       COALESCE(array_agg(g.name::text ORDER BY g.name) FILTER (WHERE g.id IS NOT NULL), '{}')
FROM movies m
LEFT JOIN movie_genres mg ON mg.movie_id = m.id
LEFT JOIN genres g ON g.id = mg.genre_id
WHERE m.deleted_at IS NULL`

func scanMovie(row pgx.Row) (Movie, error) {
	var m Movie
	err := row.Scan(&m.ID, &m.Title, &m.Description, &m.PosterURL, &m.DurationMinutes, &m.CreatedAt, &m.UpdatedAt, &m.Genres)
	if errors.Is(err, pgx.ErrNoRows) {
		return Movie{}, apperr.NotFound("movie not found")
	}
	return m, err
}

func (s *Store) Get(ctx context.Context, id int64) (Movie, error) {
	return scanMovie(s.db.QueryRow(ctx, selectMovie+` AND m.id = $1 GROUP BY m.id`, id))
}

func (s *Store) List(ctx context.Context, genre, search string, p httpx.Page) ([]Movie, error) {
	rows, err := s.db.Query(ctx, selectMovie+`
  AND ($1 = '' OR EXISTS (
        SELECT 1 FROM movie_genres mg2 JOIN genres g2 ON g2.id = mg2.genre_id
        WHERE mg2.movie_id = m.id AND lower(g2.name::text) = lower($1)))
  AND ($2 = '' OR m.title ILIKE '%' || $2 || '%')
GROUP BY m.id
ORDER BY m.title, m.id
LIMIT $3 OFFSET $4`, genre, search, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, in Input) (Movie, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO movies (title, description, poster_url, duration_minutes) VALUES ($1, $2, $3, $4) RETURNING id`,
			in.Title, in.Description, in.PosterURL, in.DurationMinutes).Scan(&id); err != nil {
			return err
		}
		return setGenres(ctx, tx, id, in.Genres)
	})
	if err != nil {
		return Movie{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Update(ctx context.Context, id int64, in Input) (Movie, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
UPDATE movies SET title = $2, description = $3, poster_url = $4, duration_minutes = $5, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`, id, in.Title, in.Description, in.PosterURL, in.DurationMinutes)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("movie not found")
		}
		return setGenres(ctx, tx, id, in.Genres)
	})
	if err != nil {
		return Movie{}, err
	}
	return s.Get(ctx, id)
}

// Delete soft-deletes a movie. Movies with upcoming showtimes must have them cancelled first,
// so customers holding tickets are never silently orphaned.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var upcoming bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM showtimes WHERE movie_id = $1 AND status = 'scheduled' AND ends_at > now())`, id).Scan(&upcoming); err != nil {
			return err
		}
		if upcoming {
			return apperr.Conflict("movie has scheduled showtimes; cancel them first")
		}
		tag, err := tx.Exec(ctx, `UPDATE movies SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("movie not found")
		}
		return nil
	})
}

func setGenres(ctx context.Context, tx pgx.Tx, movieID int64, genres []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM movie_genres WHERE movie_id = $1`, movieID); err != nil {
		return err
	}
	if len(genres) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO genres (name) SELECT unnest($1::text[]) ON CONFLICT (name) DO NOTHING`, genres); err != nil {
		return err
	}
	lower := make([]string, len(genres))
	for i, g := range genres {
		lower[i] = strings.ToLower(g)
	}
	_, err := tx.Exec(ctx, `
INSERT INTO movie_genres (movie_id, genre_id)
SELECT $1, id FROM genres WHERE lower(name::text) = ANY($2::text[])`, movieID, lower)
	return err
}

type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (s *Store) Genres(ctx context.Context) ([]Genre, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name::text FROM genres ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Genre, error) {
		var g Genre
		err := row.Scan(&g.ID, &g.Name)
		return g, err
	})
}
