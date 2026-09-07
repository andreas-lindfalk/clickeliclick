package app

import (
	"time"
)

// Event is a single row in poc.events.
type Event struct {
	TS        time.Time `json:"ts"`
	UserID    uint64    `json:"user_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
}
