-- +goose Up
-- Rollup target. Each row holds *partial aggregate states* for one minute and
-- event type: a count state and a uniq(user_id) sketch. AggregatingMergeTree
-- merges rows with the same sort key by merging the states, so the table
-- stays at roughly one row per minute per type no matter how many parts
-- were inserted.
CREATE TABLE IF NOT EXISTS events_per_minute
(
    minute     DateTime,
    event_type LowCardinality(String),
    events     AggregateFunction(count),
    users      AggregateFunction(uniq, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (event_type, minute);

-- The materialized view is an insert trigger on events: every inserted batch
-- is run through this SELECT and the result is written to events_per_minute.
-- It never reads existing rows of events, hence the backfill below.
CREATE MATERIALIZED VIEW IF NOT EXISTS events_per_minute_mv TO events_per_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    event_type,
    countState()        AS events,
    uniqState(user_id)  AS users
FROM events
GROUP BY minute, event_type;

-- Backfill what was inserted before the view existed. On a live system rows
-- inserted between the two statements above and this one would be counted
-- twice; the production pattern is a fixed cutoff timestamp in both the view
-- (WHERE ts >= cutoff) and the backfill (WHERE ts < cutoff).
INSERT INTO events_per_minute
SELECT toStartOfMinute(ts), event_type, countState(), uniqState(user_id)
FROM events
GROUP BY 1, 2;

-- +goose Down
DROP VIEW IF EXISTS events_per_minute_mv;
DROP TABLE IF EXISTS events_per_minute;
