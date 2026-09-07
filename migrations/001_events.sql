-- +goose Up
CREATE TABLE IF NOT EXISTS events
(
    ts         DateTime64(3) DEFAULT now64(3),
    user_id    UInt64,
    event_type LowCardinality(String),
    payload    String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (event_type, user_id, ts);

-- +goose Down
DROP TABLE IF EXISTS events;
