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

// User is a row of the users dimension table (ReplacingMergeTree, migration 006).
type User struct {
	UserID    uint64    `json:"user_id"`
	Country   string    `json:"country"`
	Plan      string    `json:"plan"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CountryCount is one row of EventsByCountry.
type CountryCount struct {
	Country string `json:"country"`
	Events  uint64 `json:"events"`
}

// Funnel counts users who reached each step of view -> click -> purchase, in
// order, within a time window per user.
type Funnel struct {
	Viewed    uint64 `json:"viewed"`
	Clicked   uint64 `json:"clicked"`
	Purchased uint64 `json:"purchased"`
}

// Retention counts, for users active on Day, how many were active again on
// each following day. Days[0] is Day itself, Days[1] the day after, and so on.
type Retention struct {
	Day  time.Time `json:"day"`
	Days []uint64  `json:"days"`
}

// TopUser is a user ranked by event count within their country.
type TopUser struct {
	Country  string `json:"country"`
	UserID   uint64 `json:"user_id"`
	Events   uint64 `json:"events"`
	LastPage string `json:"last_page"`
}
