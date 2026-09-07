package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/pressly/goose/v3"

	"clickeliclick/migrations"
)

// Migrate applies all pending migrations from the embedded migrations package.
// goose records what it has applied in a goose_db_version table, so calling
// this on every startup is safe and idempotent.
//
// goose works over database/sql, while the Client uses the native driver
// interface, so this opens its own short-lived connection.
func Migrate(ctx context.Context, cfg Config) error {
	db := clickhouse.OpenDB(options(cfg))
	defer db.Close()

	p, err := goose.NewProvider(goose.DialectClickHouse, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
