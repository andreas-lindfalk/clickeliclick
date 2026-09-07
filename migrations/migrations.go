// Package migrations embeds the SQL schema files so the service and the test
// helper apply exactly the same migrations, in order, with goose.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
