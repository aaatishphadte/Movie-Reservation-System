-- Reference data: genres and two halls with generated seat layouts (last 2 rows are premium).

INSERT INTO genres (name)
VALUES ('Action'), ('Adventure'), ('Animation'), ('Comedy'), ('Drama'),
       ('Horror'), ('Romance'), ('Sci-Fi'), ('Thriller'), ('Documentary');

INSERT INTO halls (name, capacity)
VALUES ('Hall 1', 100), ('Hall 2', 60);

INSERT INTO seats (hall_id, row_label, seat_number, seat_type)
SELECT h.id,
       chr(64 + r),
       n,
       CASE WHEN r > cfg.rows - 2 THEN 'premium'::seat_type ELSE 'standard'::seat_type END
FROM (VALUES ('Hall 1', 10, 10), ('Hall 2', 6, 10)) AS cfg (name, rows, per_row)
JOIN halls h ON h.name = cfg.name
CROSS JOIN LATERAL generate_series(1, cfg.rows) AS r
CROSS JOIN LATERAL generate_series(1, cfg.per_row) AS n;
