-- +goose Up
-- Turn the payload from an opaque string into the native JSON type. ClickHouse
-- stores every JSON path as its own column, so a filter on payload.ref reads
-- only that path instead of parsing every document.
--
-- Paths we know about get type hints and become ordinary typed columns.
-- Any other key that shows up in the data is stored as a dynamic subcolumn
-- with its type inferred, up to max_dynamic_paths (default 1024) after which
-- rare paths are packed together in a shared column.
--
-- This is a mutation that rewrites the column for every part (and the
-- projection); mutations_sync = 1 makes the migration wait for it.
ALTER TABLE events MODIFY COLUMN payload JSON(page String, ref LowCardinality(String))
    SETTINGS mutations_sync = 1;

-- +goose Down
ALTER TABLE events MODIFY COLUMN payload String SETTINGS mutations_sync = 1;
