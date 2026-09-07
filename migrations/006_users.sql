-- +goose Up
-- A dimension table that gets updated. ClickHouse parts are immutable, so an
-- "update" is an insert of a new version plus a merge rule: ReplacingMergeTree
-- keeps only the row with the highest updated_at per sort key when parts
-- merge. Until then both versions exist, and readers must either use FINAL
-- (merge at read time) or pick the newest themselves with argMax.
CREATE TABLE IF NOT EXISTS users
(
    user_id    UInt64,
    country    LowCardinality(String),
    plan       LowCardinality(String),
    updated_at DateTime64(3) DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY user_id;

-- A dictionary is an in-memory lookup table refreshed from a source on a
-- schedule (here every 30-60 s, or on SYSTEM RELOAD DICTIONARY). dictGet on
-- it is a hash lookup, far cheaper than joining users into a 5M row scan.
-- The source query uses FINAL so a half-merged users table still loads one
-- version per key. Dictionary sources need a qualified table name, hence the
-- hard-coded database.
CREATE DICTIONARY IF NOT EXISTS users_dict
(
    user_id UInt64,
    country String,
    plan    String
)
PRIMARY KEY user_id
SOURCE(CLICKHOUSE(QUERY 'SELECT user_id, country, plan FROM poc.users FINAL'))
LAYOUT(HASHED())
LIFETIME(MIN 30 MAX 60);

-- +goose Down
DROP DICTIONARY IF EXISTS users_dict;
DROP TABLE IF EXISTS users;
