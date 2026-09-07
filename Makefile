.PHONY: up down run logs sql

up:        ## start ClickHouse
	docker compose up -d

down:      ## stop ClickHouse and drop its data
	docker compose down -v

logs:
	docker compose logs -f clickhouse

run:       ## run the Go service
	go run ./cmd/server

sql:       ## open an interactive clickhouse-client
	docker compose exec clickhouse clickhouse-client --database poc

test:      ## run integration tests (needs Docker)
	go test ./... -v -count=1
