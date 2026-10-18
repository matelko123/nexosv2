CREATE TABLE users (
    id             INTEGER PRIMARY KEY,
    username       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash  TEXT    NOT NULL,
    totp_secret    TEXT    NOT NULL,
    totp_last_step INTEGER NOT NULL DEFAULT 0,
    role           TEXT    NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    created_at     INTEGER NOT NULL,
    last_login_at  INTEGER
);

CREATE TABLE sessions (
    token_hash   BLOB    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip           TEXT,
    user_agent   TEXT
);

CREATE TABLE agents (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    address      TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    enrolled_at  INTEGER,
    hostname     TEXT,
    info_json    TEXT,
    last_seen_at INTEGER
);

CREATE TABLE agent_enrollments (
    agent_id    TEXT    PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    secret_hash BLOB    NOT NULL,
    expires_at  INTEGER NOT NULL
);

CREATE TABLE agent_certs (
    fingerprint TEXT    PRIMARY KEY,
    agent_id    TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    serial      TEXT    NOT NULL,
    not_after   INTEGER NOT NULL,
    issued_at   INTEGER NOT NULL,
    revoked_at  INTEGER
);
CREATE INDEX agent_certs_agent ON agent_certs(agent_id);

CREATE TABLE audit_log (
    id       INTEGER PRIMARY KEY,
    ts       INTEGER NOT NULL,
    user_id  INTEGER,
    username TEXT,
    ip       TEXT,
    action   TEXT    NOT NULL,
    target   TEXT,
    agent_id TEXT,
    success  INTEGER NOT NULL,
    detail   TEXT
);

CREATE TABLE host_metrics (
    agent_id   TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    ts         INTEGER NOT NULL,
    cpu        REAL,
    mem_used   INTEGER,
    mem_total  INTEGER,
    swap_used  INTEGER,
    load1      REAL,
    net_rx     REAL,
    net_tx     REAL,
    disk_read  REAL,
    disk_write REAL,
    PRIMARY KEY (agent_id, ts)
) WITHOUT ROWID;
