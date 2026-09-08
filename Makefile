.PHONY: up down run logs sql sql-agent chat audit

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

audit:     ## what the agent ran in one conversation: make audit ID=chat-1a2b3c4d
	docker compose exec clickhouse clickhouse-client --database poc -q "SELECT event_time, query_duration_ms, read_rows, exception_code, query FROM system.query_log WHERE user = 'agent' AND log_comment = '$(ID)' AND type != 'QueryStart' ORDER BY event_time FORMAT Vertical"

sql-agent: ## same, as the restricted user the LLM agent will use
	docker compose exec clickhouse clickhouse-client --database poc --user agent --password agent

test:      ## run integration tests (needs Docker)
	go test ./... -v -count=1

seed:      ## load synthetic events (ROWS=..., BATCH=... to override)
	go run ./cmd/seed -rows $(or $(ROWS),5000000) -batch $(or $(BATCH),500000)
