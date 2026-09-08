package clickhouse

import "os"

// Config is what both the native client and the migrator need to connect.
type Config struct {
	Addr     string
	Database string
	User     string
	Password string
}

// ConfigFromEnv reads CLICKHOUSE_* variables, falling back to the local
// docker-compose defaults.
func ConfigFromEnv() Config {
	return Config{
		Addr:     env("CLICKHOUSE_ADDR", "localhost:9000"),
		Database: env("CLICKHOUSE_DB", "poc"),
		User:     env("CLICKHOUSE_USER", "default"),
		Password: env("CLICKHOUSE_PASSWORD", ""),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// AgentConfigFromEnv is ConfigFromEnv with the restricted user from
// migration 007. CLICKHOUSE_AGENT_USER and CLICKHOUSE_AGENT_PASSWORD
// override the defaults baked into that migration.
func AgentConfigFromEnv() Config {
	cfg := ConfigFromEnv()
	cfg.User = env("CLICKHOUSE_AGENT_USER", "agent")
	cfg.Password = env("CLICKHOUSE_AGENT_PASSWORD", "agent")
	return cfg
}
