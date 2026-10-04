-- Reproducible load-test seed for the items table.
--
-- Usage:
--   psql "$DATABASE_URL" -f loadtest/seed.sql                 -- 100k rows (default)
--   psql "$DATABASE_URL" -v n=1000000 -f loadtest/seed.sql    -- custom row count
--   make load-seed                 (N defaults to 100000)
--   make load-seed N=1000000
--
-- Appends N active rows then ANALYZE so the planner has fresh stats. Rerunnable
-- (appends each time) — TRUNCATE first if you want an exact count.

\if :{?n}
\else
  \set n 100000
\endif

INSERT INTO items (notes)
SELECT jsonb_build_object('seed', g)
FROM generate_series(1, :n) g;

ANALYZE items;

SELECT count(*) AS total_items FROM items;
