# clickeliclick

Minimal Go + ClickHouse playground.

## Layout

- `cmd/server/main.go` — tiny HTTP service (insert / query / aggregate)
- `internal/clickhouse/client.go` — ClickHouse client wrapper (native protocol, batch inserts, parameterised queries)
- `migrations/001_events.sql` — schema, auto-applied by the container on first start
- `docker-compose.yml` — local ClickHouse server

## Run

```sh
make up        # start ClickHouse (ports 9000 native, 8123 http)
make run       # start the service on :8080
```

Poke it:

```sh
curl -X POST localhost:8080/events -d '[{"user_id":1,"event_type":"click","payload":"{}"},{"user_id":2,"event_type":"view","payload":"{}"}]'
curl localhost:8080/events
curl localhost:8080/stats
```

Explore directly:

```sh
make sql                                  # clickhouse-client inside the container
open http://localhost:8123/play           # web query UI
open http://localhost:8123/dashboard      # web dashboard UI with metrics
```

`make down` stops the server and wipes the data volume, so the migration reruns next time.

## Config

| env                   | default          |
|-----------------------|------------------|
| `CLICKHOUSE_ADDR`     | `localhost:9000` |
| `CLICKHOUSE_DB`       | `poc`            |
| `CLICKHOUSE_USER`     | `default`        |
| `CLICKHOUSE_PASSWORD` | empty            |
| `HTTP_ADDR`           | `:8080`          |

## Tests

Integration tests live in `internal/clickhouse/client_test.go`. They start a throwaway ClickHouse via
[testcontainers-go](https://golang.testcontainers.org/modules/clickhouse/), apply `migrations/001_events.sql`,
and exercise the client with `testify/require`. One container is shared across the package; each test
truncates the table first.

```sh
make test      # needs Docker running
```
