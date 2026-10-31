CREATE TABLE IF NOT EXISTS aegis_spend (
    team     text   NOT NULL,
    month    text   NOT NULL,
    nano_usd bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (team, month)
);

-- The audit log: who used which model, at what cost, and what the gateway decided.
-- Prompt and completion text are deliberately never stored.
CREATE TABLE IF NOT EXISTS aegis_requests (
    id                text        PRIMARY KEY,
    at                timestamptz NOT NULL,
    team              text        NOT NULL,
    key_id            text        NOT NULL,
    model             text        NOT NULL,
    upstream          text        NOT NULL,
    status            integer     NOT NULL,
    prompt_tokens     integer     NOT NULL,
    completion_tokens integer     NOT NULL,
    nano_usd          bigint      NOT NULL,
    cached            boolean     NOT NULL,
    pii               jsonb       NOT NULL,
    decision          text        NOT NULL,
    latency_ms        integer     NOT NULL
);

CREATE INDEX IF NOT EXISTS aegis_requests_by_team ON aegis_requests (team, at DESC);
