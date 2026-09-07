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
curl -X DELETE localhost:8080/users/42/events               # erasure request: a mutation
curl -X PUT localhost:8080/users/7 -d '{"country":"IS","plan":"team"}'   # "update": insert a new version
curl localhost:8080/users/7                                 # read with FINAL
curl "localhost:8080/stats/countries"                       # dictGet lookup per event
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

## Data lifecycle: TTL, partitions, deletes

ClickHouse parts are immutable, so nothing is ever changed in place. Everything in this section is
either "drop a whole part" (cheap) or "rewrite the part without some rows" (a *mutation*, expensive).

**Retention.** Migration 005 adds `TTL ts + INTERVAL 90 DAY` to `events`. TTL is enforced when a
part is written or merged, not on a timer: a row already expired at insert never lands, a row that
expires later lingers until its part is merged, and TTL merges run at most every
`merge_with_ttl_timeout` seconds (4h). With `ttl_only_drop_parts = 1` and monthly partitions, old
data ages out by dropping whole parts. The rollup has no TTL: aggregates outlive raw rows.

**Partitions.** `ALTER TABLE events DROP PARTITION '202608'` removes a month instantly, which is why
the partition key should match how you delete, not how you query.

**Row deletes.** `Repository.DeleteUserEvents` runs `ALTER TABLE events DELETE WHERE user_id = ?`, a
classic mutation: every part containing the user is rewritten, the projection with it, and the data
is physically gone when it returns. On the 5M seeded rows that took 1.3 s for 53 rows, because it
touched all ten parts. Right for rare, must-be-thorough erasure requests; wrong for anything frequent.

ClickHouse also has *lightweight* deletes, `DELETE FROM events WHERE ...`, which only write a
`_row_exists` mask and let merges remove the rows later. This table refuses them on purpose. Its
default `lightweight_mutation_projection_mode = 'throw'` blocks lightweight deletes because the
`by_user` projection would keep serving the deleted rows. Verified here on 26.8: with `'rebuild'` the
projection-backed query returned the deleted rows while the main table did not, and with `'drop'`
the affected parts lose the projection and can no longer merge with parts that have it. Lightweight
deletes and projections do not mix yet.

**The rollup drifts.** A materialized view only sees inserts, so after an erasure `events_per_minute`
still counts the deleted rows. Aggregates without identity are usually fine to keep; if not, rebuild
the affected minutes from `events`.

Try it yourself:

```sql
-- what mutations ran, and whether any is stuck (a failed one blocks those behind it)
SELECT command, create_time, is_done, parts_to_do, latest_fail_reason
FROM system.mutations WHERE table = 'events' ORDER BY create_time DESC LIMIT 5;

-- the mutation version is the last number in each part name; it changes on every rewrite
SELECT name, rows, modification_time FROM system.parts WHERE table = 'events' AND active;

-- expired rows never land
INSERT INTO events (ts, user_id, event_type, payload) VALUES (now() - INTERVAL 100 DAY, 1, 'view', '{}');
SELECT count() FROM events WHERE ts < now() - INTERVAL 90 DAY;

-- when is a partition due to expire entirely?
SELECT partition, min(delete_ttl_info_min) AS first_expiry, max(delete_ttl_info_max) AS last_expiry
FROM system.parts WHERE table = 'events' AND active GROUP BY partition;

-- the drift between raw and rollup after a delete
SELECT (SELECT count() FROM events) AS raw,
       (SELECT sum(c) FROM (SELECT countMerge(events) AS c FROM events_per_minute GROUP BY minute, event_type)) AS rollup;
```

## Updates and lookups: ReplacingMergeTree and dictionaries

Parts are immutable, so ClickHouse has no in-place update. Migration 006 adds a `users` table on
`ReplacingMergeTree(updated_at)`: an update is an insert of a new version, and the engine keeps only
the newest version per `user_id` when parts merge. Until they merge, both versions are on disk, and
the read side has to cope, the same pattern as the rollup in the materialized-view section:

- `SELECT ... FROM users FINAL` applies the merge rule at read time. Correct, simple, and it has to
  read every version of every matching key. `Repository.GetUser` uses it for point lookups.
- `argMax(country, updated_at) ... GROUP BY user_id` does the same by hand and can be cheaper for
  scans, since it is an ordinary aggregation.
- Waiting for merges is not an option: nothing guarantees when, or that, the last merge happens.

`users_dict` is a **dictionary**, an in-memory hash table refreshed from the `users` table every
30 to 60 seconds, or on `SYSTEM RELOAD DICTIONARY users_dict`. Its source query uses `FINAL`, so it
loads one version per key. `dictGet('users_dict', 'country', user_id)` is a hash lookup per row,
which is how `Repository.EventsByCountry` enriches five million events without a join. It is a
snapshot, so an updated user is served stale until the next reload; the test for it shows exactly that.

Measured on the seeded data, 100k users and 5M events:

| query | rows read | time | memory |
|---|---|---|---|
| events per country via `dictGet` | 5.0M | 26 ms | 4.4 MiB |
| events per country via `JOIN users FINAL` | 5.1M | 57 ms | 22.6 MiB |
| `count() FROM users` | 1 | 1 ms | |
| `count() FROM users FINAL` | 108k | 3 ms | |

The dictionary holds 100k users in 13 MiB and loads in under 30 ms. Dictionaries suit dimension
data that fits in memory and changes slowly; a join is the tool when neither holds.

Try it yourself:

```sql
-- both versions of an updated user are on disk until a merge
SELECT user_id, country, plan, updated_at, _part FROM users WHERE user_id = 7;
SELECT user_id, country, plan FROM users FINAL WHERE user_id = 7;
OPTIMIZE TABLE users FINAL;   -- now the first query returns one row too

-- what the dictionary looks like from the inside
SELECT name, status, element_count, formatReadableSize(bytes_allocated) AS memory,
       loading_duration, last_successful_update_time FROM system.dictionaries;

-- the same lookup, three ways
SELECT dictGet('users_dict', 'country', toUInt64(7));
SELECT country FROM users FINAL WHERE user_id = 7;
SELECT argMax(country, updated_at) FROM users WHERE user_id = 7;
```

## Tests

Integration tests live in `internal/app/repository_test.go`. They start a throwaway ClickHouse via
[testcontainers-go](https://golang.testcontainers.org/modules/clickhouse/), run the embedded migrations,
and exercise the client with `testify/require`. One container is shared across the package; each test
truncates the table first.

```sh
make test      # needs Docker running
```
