package app

import (
	"encoding/json"
	"time"
)

// Event is a single row in poc.events.
type Event struct {
	TS        time.Time `json:"ts"`
	UserID    uint64    `json:"user_id"`
	EventType string    `json:"event_type"`
	// Payload is stored in a ClickHouse JSON column; see migration 004.
	Payload json.RawMessage `json:"payload"`
}

// EventColumns holds a batch of events column by column, which is how
// ClickHouse stores and transfers data. All slices must have the same length.
type EventColumns struct {
	TS        []time.Time
	UserID    []uint64
	EventType []string
	Payload   []string
}

// MinuteStats is one row of the events_per_minute rollup, with the aggregate
// states already merged into plain numbers.
type MinuteStats struct {
	Minute    time.Time `json:"minute"`
	EventType string    `json:"event_type"`
	Events    uint64    `json:"events"`
	Users     uint64    `json:"users"`
}

// PageCount is one row of TopPagesByRef.
type PageCount struct {
	Page  string `json:"page"`
	Count uint64 `json:"count"`
}
