-- Core schema. Integrity rules live in the database, not only in application code:
--   * showtimes_no_overlap      -> a hall can never be double-scheduled
--   * reservation_seats_no_double_booking -> a seat can never be sold twice for a showtime

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TYPE user_role          AS ENUM ('user', 'admin');
CREATE TYPE seat_type          AS ENUM ('standard', 'premium');
CREATE TYPE showtime_status    AS ENUM ('scheduled', 'cancelled');
CREATE TYPE reservation_status AS ENUM ('confirmed', 'cancelled');

CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         CITEXT      NOT NULL UNIQUE,
    name          TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          user_role   NOT NULL DEFAULT 'user',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE genres (
    id   INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name CITEXT NOT NULL UNIQUE
);

CREATE TABLE movies (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title            TEXT        NOT NULL,
    description      TEXT        NOT NULL DEFAULT '',
    poster_url       TEXT        NOT NULL DEFAULT '',
    duration_minutes INT         NOT NULL CHECK (duration_minutes BETWEEN 1 AND 600),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ -- soft delete keeps historical reservations/reports intact
);
CREATE INDEX movies_title_active_idx ON movies (title) WHERE deleted_at IS NULL;

CREATE TABLE movie_genres (
    movie_id BIGINT NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    genre_id INT    NOT NULL REFERENCES genres (id) ON DELETE CASCADE,
    PRIMARY KEY (movie_id, genre_id)
);
CREATE INDEX movie_genres_genre_idx ON movie_genres (genre_id);

CREATE TABLE halls (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    capacity   INT         NOT NULL CHECK (capacity > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE seats (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hall_id     BIGINT    NOT NULL REFERENCES halls (id) ON DELETE CASCADE,
    row_label   TEXT      NOT NULL,
    seat_number INT       NOT NULL CHECK (seat_number > 0),
    seat_type   seat_type NOT NULL DEFAULT 'standard',
    UNIQUE (hall_id, row_label, seat_number)
);

CREATE TABLE showtimes (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    movie_id    BIGINT          NOT NULL REFERENCES movies (id),
    hall_id     BIGINT          NOT NULL REFERENCES halls (id),
    starts_at   TIMESTAMPTZ     NOT NULL,
    ends_at     TIMESTAMPTZ     NOT NULL, -- start + runtime + cleaning buffer
    price_cents INT             NOT NULL CHECK (price_cents >= 0),
    status      showtime_status NOT NULL DEFAULT 'scheduled',
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    CONSTRAINT showtimes_no_overlap EXCLUDE USING gist (
        hall_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (status = 'scheduled')
);
CREATE INDEX showtimes_starts_at_idx ON showtimes (starts_at) WHERE status = 'scheduled';
CREATE INDEX showtimes_movie_idx ON showtimes (movie_id);

CREATE TABLE reservations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         BIGINT             NOT NULL REFERENCES users (id),
    showtime_id     BIGINT             NOT NULL REFERENCES showtimes (id),
    status          reservation_status NOT NULL DEFAULT 'confirmed',
    total_cents     INT                NOT NULL DEFAULT 0,
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ        NOT NULL DEFAULT now(),
    cancelled_at    TIMESTAMPTZ,
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX reservations_user_idx ON reservations (user_id, created_at DESC);
CREATE INDEX reservations_showtime_idx ON reservations (showtime_id);

CREATE TABLE reservation_seats (
    reservation_id UUID    NOT NULL REFERENCES reservations (id) ON DELETE CASCADE,
    showtime_id    BIGINT  NOT NULL REFERENCES showtimes (id),
    seat_id        BIGINT  NOT NULL REFERENCES seats (id),
    price_cents    INT     NOT NULL CHECK (price_cents >= 0), -- price snapshot at booking time
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (reservation_id, seat_id)
);

-- The last line of defence against overbooking: at most one *active* sale per seat per showtime.
CREATE UNIQUE INDEX reservation_seats_no_double_booking
    ON reservation_seats (showtime_id, seat_id) WHERE active;
