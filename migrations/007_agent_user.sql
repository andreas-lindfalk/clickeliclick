-- +goose Up
-- The LLM agent gets its own ClickHouse identity, and the fence around what it
-- may do lives here, server-side, rather than in a SQL regex in Go. Four
-- objects: a settings profile (resource caps), a user (identity + grants),
-- row policies (which rows it may see) and a quota (how often).

-- readonly = 1 means SELECT only, and it also freezes every setting: the
-- agent cannot append SETTINGS max_execution_time = 0 to a query. The
-- exceptions marked CHANGEABLE_IN_READONLY are the two output settings our
-- driver sends with every query, and log_comment, which the tool layer sets
-- to the conversation id so system.query_log doubles as the audit trail.
-- max_result_rows keeps the default result_overflow_mode = 'throw', so an
-- oversized SELECT fails loudly and the model gets told to aggregate or LIMIT,
-- instead of silently receiving a truncated result and reasoning from it.
CREATE SETTINGS PROFILE IF NOT EXISTS agent SETTINGS
    readonly = 1,
    max_execution_time = 5,
    max_rows_to_read = 20000000,
    max_memory_usage = 1000000000,
    max_result_rows = 200,
    log_comment CHANGEABLE_IN_READONLY,
    output_format_native_write_json_as_string CHANGEABLE_IN_READONLY,
    output_format_json_escape_forward_slashes CHANGEABLE_IN_READONLY;

-- A fixed password is fine for a local POC; in real life the password comes
-- from config and the user is created out of band. Stored as a SHA-256 hash.
CREATE USER IF NOT EXISTS agent IDENTIFIED BY 'agent' SETTINGS PROFILE 'agent';

-- SELECT on exactly the tables the agent may query. Nothing on system.*, so
-- it cannot read other users' queries or the server config; system.tables
-- and system.columns still work but only list what it has grants on. Table
-- functions that reach outside the server (url, s3, remote) need their own
-- grants, which it does not get.
GRANT SELECT ON poc.events TO agent;
GRANT SELECT ON poc.events_per_minute TO agent;
GRANT SELECT ON poc.users TO agent;
GRANT dictGet ON poc.users_dict TO agent;

-- Row policies filter every SELECT the named user runs, projections
-- included. Users without a policy on the table are unaffected. A policy is
-- per table, so the rollup gets a matching one; otherwise the agent could
-- read older days as aggregates through events_per_minute.
CREATE ROW POLICY IF NOT EXISTS agent_recent ON poc.events
    FOR SELECT USING ts > now() - INTERVAL 30 DAY TO agent;
CREATE ROW POLICY IF NOT EXISTS agent_recent ON poc.events_per_minute
    FOR SELECT USING minute > now() - INTERVAL 30 DAY TO agent;

-- Rate limit per rolling hour, keyed by user name. The agent can read its
-- own usage in system.quota_usage.
CREATE QUOTA IF NOT EXISTS agent FOR INTERVAL 1 hour MAX queries = 200 TO agent;

-- +goose Down
DROP QUOTA IF EXISTS agent;
DROP ROW POLICY IF EXISTS agent_recent ON poc.events, poc.events_per_minute;
DROP USER IF EXISTS agent;
DROP SETTINGS PROFILE IF EXISTS agent;
