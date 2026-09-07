# clickeliclick

Minimal Go + ClickHouse playground.

## Layout

- `cmd/server/main.go` — tiny HTTP service (insert / query / aggregate)
- `cmd/seed/main.go` — bulk loader that fills `events` with synthetic data
- `internal/app/` — `Event` entity and the `Repository` that owns the SQL (batch inserts, parameterised queries)
- `internal/pkg/clickhouse/client.go` — thin wrapper around the native ClickHouse connection
- `internal/pkg/clickhouse/clickhousetest/` — throwaway ClickHouse container for integration tests
- `migrations/` — goose SQL migrations, embedded into the binary and applied on service startup and in tests
- `internal/pkg/clickhouse/migrate.go` — runs the embedded migrations with goose
- `docker-compose.yml` — local ClickHouse server

## Run

```sh
make up        # start ClickHouse (ports 9000 native, 8123 http)
make run       # start the service on :8080
```

Poke it:

```sh
curl -X POST localhost:8080/events -d '[{"user_id":1,"event_type":"click","payload":"{}"},{"user_id":2,"event_type":"view","payload":"{}"}]'
curl -X POST localhost:8080/event -d '{"user_id":1,"event_type":"click","payload":"{}"}'   # single row, async insert
curl localhost:8080/events
curl localhost:8080/users/42/events
curl "localhost:8080/pages?ref=google"                      # groups on a JSON path
curl localhost:8080/stats                                   # aggregates the raw table
curl "localhost:8080/stats/minutes?from=2026-09-01T00:00:00Z&to=2026-09-01T01:00:00Z"   # reads the rollup
```

Load some real volume so queries have something to work on:

```sh
make seed                    # 5M rows in batches of 500k
make seed ROWS=1000000 BATCH=1000   # many tiny inserts: watch parts pile up
```

## Getting data in

ClickHouse turns every INSERT into an immutable *part* on disk and merges parts in the
background. Thousands of tiny inserts therefore mean thousands of parts, and eventually the
`TOO_MANY_PARTS` error. Two ways to avoid that, both used here:

- **Batch on the client.** `Repository.InsertEventColumns` appends whole columns and sends one
  INSERT per batch. The seed command does this at roughly a million rows per second locally.
- **Let the server batch.** `Repository.InsertEventAsync` uses `async_insert`: ClickHouse buffers
  rows from many small inserts and flushes them as one part. `POST /event` uses it. With
  `wait_for_async_insert=1` each request waits for the flush, so expect around 200 ms latency
  in exchange for read-your-writes.

Things to look at after seeding and a burst of `POST /event` calls:

```sql
-- parts per partition, and how big they are
SELECT partition, count() AS parts, sum(rows) AS rows, formatReadableSize(sum(bytes_on_disk)) AS on_disk
FROM system.parts WHERE database = 'poc' AND table = 'events' AND active
GROUP BY partition ORDER BY partition;

-- how well each column compresses (LowCardinality event_type is ~200x)
SELECT name, formatReadableSize(data_uncompressed_bytes) AS raw, formatReadableSize(data_compressed_bytes) AS compressed,
       round(data_uncompressed_bytes / data_compressed_bytes, 1) AS ratio
FROM system.columns WHERE database = 'poc' AND table = 'events';

-- how many single-row async inserts got folded into each flush
SYSTEM FLUSH LOGS;
SELECT flush_time, count() AS inserts, sum(rows) AS rows
FROM system.asynchronous_insert_log WHERE table = 'events'
GROUP BY flush_time ORDER BY flush_time;

-- background merges at work
SELECT event_time, event_type, rows, formatReadableSize(size_in_bytes) AS size, merge_reason
FROM system.part_log WHERE table = 'events' ORDER BY event_time DESC LIMIT 20;
```

Explore directly:

```sh
make sql                                  # clickhouse-client inside the container
open http://localhost:8123/play           # web query UI
open http://localhost:8123/dashboard      # web dashboard UI with metrics
```

Migrations run every time the service starts; goose tracks what is applied in `poc.goose_db_version`.
Add a new file as `migrations/00N_name.sql` with `-- +goose Up` / `-- +goose Down` sections.

`make down` stops the server and wipes the data volume.

## Config

| env                   | default          |
|-----------------------|------------------|
| `CLICKHOUSE_ADDR`     | `localhost:9000` |
| `CLICKHOUSE_DB`       | `poc`            |
| `CLICKHOUSE_USER`     | `default`        |
| `CLICKHOUSE_PASSWORD` | empty            |
| `HTTP_ADDR`           | `:8080`          |

## Reading less

ClickHouse has no row-level index. Each part is sorted by the table's `ORDER BY`, and a sparse
primary index stores one entry per *granule* of 8192 rows. A query can only skip granules the
sort order lets it exclude, so the sort key is the single biggest performance decision.

`events` is sorted by `(event_type, user_id, ts)`. Filtering on `event_type` prunes well. Filtering
on `user_id` alone is weaker: the index is sorted by event type first, so ClickHouse falls back to a
"generic exclusion search" over the second column. Migration 002 adds a **projection**, a hidden copy
of the data inside each part sorted by `(user_id, ts)`, which the planner picks automatically.

Measured on the 5M seeded rows, `SELECT ... WHERE user_id = 42 ORDER BY ts DESC LIMIT 50`:

| | granules read | rows read | bytes | time |
|---|---|---|---|---|
| primary key only | 22 / 613 | 279k | 4.9 MiB | 18 ms |
| + bloom filter skip index on user_id | 15 / 613 | | | |
| + projection `by_user` | 8 / 613 | 115k | 1.0 MiB | 5 ms |

The projection costs about as much disk as the table itself (73 MiB on top of 74 MiB), since it is a
full second copy. Eight granules is the floor with eight parts: one granule per part, so fewer
parts means fewer reads.

Try it yourself:

```sql
-- what the planner does, and which index pruned what
EXPLAIN indexes = 1
SELECT ts, user_id, event_type, payload FROM events WHERE user_id = 42 ORDER BY ts DESC LIMIT 50;

-- force the old path to compare
EXPLAIN indexes = 1
SELECT ts, user_id, event_type, payload FROM events WHERE user_id = 42 ORDER BY ts DESC LIMIT 50
SETTINGS optimize_use_projections = 0;

-- what a query actually read, and which projection it used
SYSTEM FLUSH LOGS;
SELECT read_rows, read_bytes, query_duration_ms, ProfileEvents['SelectedMarks'] AS marks, projections
FROM system.query_log WHERE type = 'QueryFinish' AND query LIKE '%user_id = 42%'
ORDER BY event_time DESC LIMIT 3;

-- what the projection costs
SELECT name, count() AS parts, formatReadableSize(sum(bytes_on_disk)) AS on_disk
FROM system.projection_parts WHERE table = 'events' AND active GROUP BY name;

-- merge everything into one part per partition and re-run the EXPLAIN
OPTIMIZE TABLE events FINAL;

-- a skip index is the other tool; here it helps a little, a projection helps a lot
ALTER TABLE events ADD INDEX user_bf user_id TYPE bloom_filter GRANULARITY 1;
ALTER TABLE events MATERIALIZE INDEX user_bf SETTINGS mutations_sync = 1;
ALTER TABLE events DROP INDEX user_bf;
```

## Pre-aggregating with materialized views

A materialized view in ClickHouse is an **insert trigger**, not a cached query. Every batch
written to `events` also runs through the view's SELECT, and the result is appended to a target
table. The view never reads existing rows, so migration 003 backfills the target separately.

The target `events_per_minute` is an `AggregatingMergeTree` holding *partial aggregate states*:
a `countState()` and a `uniqState(user_id)` per minute and event type. Two inserts for the same
minute produce two rows of state; background merges combine rows with the same key by merging
states. That is why the rollup is always read with `GROUP BY` and the `-Merge` combinators:

```sql
SELECT minute, event_type, countMerge(events), uniqMerge(users)
FROM events_per_minute GROUP BY minute, event_type;
```

States merge across buckets as well, so the same table answers per-hour or per-day questions.

Measured on the 5M seeded rows, hourly buckets over 30 days:

| question | source | rows read | bytes | time |
|---|---|---|---|---|
| count per hour/type | `events` | 5.0M | 43 MiB | 64 ms |
| count per hour/type | rollup | 130k | 2.6 MiB | 9 ms |
| count + uniq users per hour/type | `events` | 5.0M | 81 MiB | 154 ms |
| count + uniq users per hour/type | rollup | 130k | 13 MiB | 192 ms |

The count wins outright. The uniques do not, and the reason is worth understanding: the
`users` column is 15 of the rollup's 20 MiB. Each per-minute bucket holds only ~39 users, so its
sketch is about as big as the raw ids it summarises, and merging 130k sketches costs more than
hashing 5M integers. A rollup pays off when raw rows per bucket is large; here it is 38. At a
hundred times the event volume the rollup would be the same size and the raw scan a hundred
times bigger. Coarser buckets (per hour) or dropping the uniq state are the fixes.

Try it yourself:

```sql
-- the view and its target
SHOW CREATE TABLE events_per_minute_mv;

-- partial states before and after a merge: insert the same minute twice, then
SELECT minute, event_type, count() AS state_rows FROM events_per_minute
WHERE minute >= toStartOfMinute(now()) GROUP BY minute, event_type;
OPTIMIZE TABLE events_per_minute FINAL;  -- and run the SELECT again

-- reading a state column directly gives you the binary blob, hence -Merge
SELECT minute, events, users FROM events_per_minute LIMIT 1 FORMAT Vertical;

-- what the states cost on disk
SELECT name, formatReadableSize(data_compressed_bytes) FROM system.columns
WHERE database = 'poc' AND table = 'events_per_minute';
```

## The JSON type

Migration 004 turns `payload` from `String` into `JSON(page String, ref LowCardinality(String))`.
ClickHouse stores every JSON path as its own column, so `WHERE payload.ref = 'google'` reads the
`ref` subcolumn and nothing else. The Go side keeps sending and receiving JSON text: the connection
sets `output_format_native_write_json_as_string` so the driver hands back a string, and the
`Event.Payload` field is a `json.RawMessage`.

Two kinds of paths:

- **Typed paths** are the ones named in the column definition. They are ordinary columns: fast to
  filter and group on, and present in *every* document, defaulting to `""` when the row was
  written without them. Expect `{"page":"","ref":""}` on old rows.
- **Dynamic paths** are everything else. Their type is inferred per value and they are queried with
  an explicit type, `payload.experiment.variant.:Int64`. Grouping directly on one is refused
  because its type may vary. After `max_dynamic_paths` distinct paths (default 1024), rare ones are
  packed into a shared column and get slower.

Measured on the 5M seeded rows, converting the column took 2.6 s and shrank it from 74 to
58 MiB. Top pages for one referrer:

| approach | rows read | bytes | time |
|---|---|---|---|
| `JSONExtractString(payload, 'page')` on a String column | 5.0M | 143 MiB | 163 ms |
| `payload.page` on the JSON column | 5.0M | 76 MiB | 20 ms |

Try it yourself:

```sql
-- every path the data contains, and the types seen for it
SELECT arrayJoin(distinctJSONPathsAndTypes(payload)) FROM events;

-- insert a document with keys nobody declared, then query them
INSERT INTO events (user_id, event_type, payload)
VALUES (1, 'click', '{"page":"/x","ref":"direct","experiment":{"name":"blue","variant":2}}');
SELECT payload.experiment.name, payload.experiment.variant.:Int64 FROM events
WHERE payload.experiment.variant.:Int64 = 2;

-- how the column is laid out on disk: one substream per path
SELECT substreams FROM system.parts_columns
WHERE database = 'poc' AND table = 'events' AND column = 'payload' AND active LIMIT 1 FORMAT Vertical;

-- the raw column vs a typed subcolumn in EXPLAIN
EXPLAIN header = 1 SELECT payload.ref, count() FROM events GROUP BY 1;
```

## Tests

Integration tests live in `internal/app/repository_test.go`. They start a throwaway ClickHouse via
[testcontainers-go](https://golang.testcontainers.org/modules/clickhouse/), run the embedded migrations,
and exercise the client with `testify/require`. One container is shared across the package; each test
truncates the table first.

```sh
make test      # needs Docker running
```
