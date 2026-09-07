-- +goose Up
-- Retention. TTL is enforced whenever a part is written or merged, not on a
-- timer: a row that is already expired at insert never lands, and a row that
-- expires later stays readable until its part is merged. TTL merges are
-- scheduled at most every merge_with_ttl_timeout seconds (4h by default).
--
-- ttl_only_drop_parts makes ClickHouse drop a part outright once every row
-- in it has expired instead of rewriting parts to remove rows one by one.
-- With monthly partitions and a 90 day TTL that is how nearly all data ages
-- out. The rollup events_per_minute has no TTL on purpose: aggregates are
-- kept for longer than the raw rows.
--
-- Deletes: see Repository.DeleteUserEvents. The table keeps the default
-- lightweight_mutation_projection_mode = 'throw', which makes lightweight
-- DELETE FROM refuse to run here; with the by_user projection present it
-- would return deleted rows from projection-backed reads (ClickHouse 26.8).
ALTER TABLE events MODIFY SETTING ttl_only_drop_parts = 1;
ALTER TABLE events MODIFY TTL ts + INTERVAL 90 DAY SETTINGS mutations_sync = 1;

-- +goose Down
ALTER TABLE events REMOVE TTL;
ALTER TABLE events RESET SETTING ttl_only_drop_parts;
