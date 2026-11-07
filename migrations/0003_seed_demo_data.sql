-- Demo catalogue so the API is explorable right after `docker compose up`.
-- Showtimes are created relative to the migration time (tomorrow and the day after, UTC).

INSERT INTO movies (title, description, poster_url, duration_minutes)
VALUES ('Orbital Drift', 'A salvage crew finds something alive in a derelict station.', 'https://picsum.photos/seed/orbital/400/600', 128),
       ('The Last Orchard', 'Three generations fight to save the family farm.', 'https://picsum.photos/seed/orchard/400/600', 112),
       ('Midnight Protocol', 'An analyst uncovers a conspiracy inside her own agency.', 'https://picsum.photos/seed/protocol/400/600', 141);

INSERT INTO movie_genres (movie_id, genre_id)
SELECT m.id, g.id
FROM (VALUES ('Orbital Drift', 'Sci-Fi'), ('Orbital Drift', 'Thriller'),
             ('The Last Orchard', 'Drama'),
             ('Midnight Protocol', 'Thriller'), ('Midnight Protocol', 'Action')) AS x (title, genre)
JOIN movies m ON m.title = x.title
JOIN genres g ON g.name = x.genre;

INSERT INTO showtimes (movie_id, hall_id, starts_at, ends_at, price_cents)
SELECT m.id,
       h.id,
       slot.starts_at,
       slot.starts_at + make_interval(mins => m.duration_minutes + 15),
       x.price
FROM (VALUES ('Orbital Drift',     'Hall 1', 1, '14:00', 1200),
             ('Midnight Protocol', 'Hall 1', 1, '18:00', 1400),
             ('Orbital Drift',     'Hall 1', 1, '21:30', 1400),
             ('The Last Orchard',  'Hall 2', 1, '15:00', 1000),
             ('The Last Orchard',  'Hall 2', 1, '19:00', 1100),
             ('Midnight Protocol', 'Hall 2', 2, '20:00', 1400)) AS x (title, hall, day_offset, at, price)
JOIN movies m ON m.title = x.title
JOIN halls h ON h.name = x.hall
CROSS JOIN LATERAL (
    SELECT (date_trunc('day', now() AT TIME ZONE 'UTC') + make_interval(days => x.day_offset) + x.at::time)
               AT TIME ZONE 'UTC' AS starts_at
) AS slot;
