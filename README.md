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
curl localhost:8080/stats
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

## Tests

Integration tests live in `internal/app/repository_test.go`. They start a throwaway ClickHouse via
[testcontainers-go](https://golang.testcontainers.org/modules/clickhouse/), run the embedded migrations,
and exercise the client with `testify/require`. One container is shared across the package; each test
truncates the table first.

```sh
make test      # needs Docker running
```
