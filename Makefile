.PHONY: up down run logs sql sql-agent chat

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

chat:      ## talk to the data through an LLM (needs ANTHROPIC_API_KEY)
	go run ./cmd/chat

sql-agent: ## same, as the restricted user the LLM agent will use
	docker compose exec clickhouse clickhouse-client --database poc --user agent --password agent

test:      ## run integration tests (needs Docker)
	go test ./... -v -count=1

seed:      ## load synthetic events (ROWS=..., BATCH=... to override)
	go run ./cmd/seed -rows $(or $(ROWS),5000000) -batch $(or $(BATCH),500000)
