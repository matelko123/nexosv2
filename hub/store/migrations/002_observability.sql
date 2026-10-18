-- Downsampled host metrics: 1-minute and 5-minute averages of host_metrics.
CREATE TABLE host_metrics_1m (
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

CREATE TABLE host_metrics_5m (
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

-- Per-container 1-minute averages, keyed by name so history survives
-- container re-creation (compose up, image updates).
CREATE TABLE container_metrics (
    agent_id  TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name      TEXT    NOT NULL,
    ts        INTEGER NOT NULL,
    cpu       REAL,
    mem       INTEGER,
    mem_limit INTEGER,
    net_rx    REAL,
    net_tx    REAL,
    PRIMARY KEY (agent_id, name, ts)
) WITHOUT ROWID;

CREATE TABLE image_updates (
    agent_id         TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    reference        TEXT    NOT NULL,
    display          TEXT    NOT NULL,
    local_image_id   TEXT,
    remote_digest    TEXT,
    update_available INTEGER NOT NULL DEFAULT 0,
    error            TEXT,
    containers_json  TEXT,
    checked_at       INTEGER NOT NULL,
    PRIMARY KEY (agent_id, reference)
);

CREATE TABLE alert_channels (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    type        TEXT    NOT NULL CHECK (type IN ('ntfy', 'discord', 'telegram', 'webhook', 'email')),
    config      TEXT    NOT NULL, -- sealed JSON, contains tokens/passwords
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL
);

CREATE TABLE alert_rules (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    type        TEXT    NOT NULL,
    agent_id    TEXT    REFERENCES agents(id) ON DELETE CASCADE, -- NULL: all hosts
    threshold   REAL    NOT NULL DEFAULT 0,
    duration_s  INTEGER NOT NULL DEFAULT 0,
    target      TEXT    NOT NULL DEFAULT '', -- container name glob / mountpoint
    channels    TEXT    NOT NULL DEFAULT '[]', -- JSON array of channel ids
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL
);

CREATE TABLE alert_events (
    id          INTEGER PRIMARY KEY,
    rule_id     INTEGER NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    agent_id    TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    subject     TEXT    NOT NULL DEFAULT '',
    value       REAL,
    message     TEXT    NOT NULL,
    started_at  INTEGER NOT NULL,
    resolved_at INTEGER
);
CREATE INDEX alert_events_open ON alert_events(resolved_at);
CREATE INDEX alert_events_started ON alert_events(started_at);

-- Sensible defaults; they show in the UI until notification channels are added.
INSERT INTO alert_rules (name, type, threshold, duration_s, target, created_at) VALUES
    ('Host offline',                 'host_offline',        0,  120, '',  strftime('%s', 'now')),
    ('Wysokie obciążenie CPU',       'host_cpu',            90, 600, '',  strftime('%s', 'now')),
    ('Mało pamięci RAM',             'host_memory',         90, 600, '',  strftime('%s', 'now')),
    ('Kończy się miejsce na dysku',  'host_disk',           90, 0,   '',  strftime('%s', 'now')),
    ('Wysoka temperatura',           'host_temperature',    85, 300, '',  strftime('%s', 'now')),
    ('Kontener ma problem',          'container_unhealthy', 0,  120, '*', strftime('%s', 'now')),
    ('Dostępna aktualizacja obrazu', 'image_update',        0,  0,   '*', strftime('%s', 'now'));
