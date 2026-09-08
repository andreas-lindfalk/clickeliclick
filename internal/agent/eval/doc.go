// Package eval holds the agent's eval set: a few questions with known
// answers, run against the real model and the seeded local database.
//
// It is behind the "eval" build tag, so `go test ./...` never compiles it.
// Run it deliberately with `make eval`; it costs API calls.
package eval
