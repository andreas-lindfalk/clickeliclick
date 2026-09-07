-- +goose Up
-- The table is sorted by (event_type, user_id, ts), which is good for
-- "all events of one type" but weak for "everything one user did".
-- A projection is a hidden copy of the table kept in a different sort order
-- inside each part. ClickHouse picks it automatically when it would read less.
ALTER TABLE events ADD PROJECTION by_user (SELECT * ORDER BY (user_id, ts));

-- ADD only applies to new inserts. MATERIALIZE builds it for existing parts
-- as a background mutation; mutations_sync = 1 makes this statement wait for it.
ALTER TABLE events MATERIALIZE PROJECTION by_user SETTINGS mutations_sync = 1;

-- +goose Down
ALTER TABLE events DROP PROJECTION IF EXISTS by_user;
