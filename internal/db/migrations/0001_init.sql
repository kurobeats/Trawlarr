-- Trawlarr schema v1.
-- Timestamps are unix seconds (UTC) so comparisons and ranges are unambiguous.

CREATE TABLE orgs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id        INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    email         TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    totp_secret   TEXT NOT NULL DEFAULT '',
    totp_enabled  INTEGER NOT NULL DEFAULT 0,
    api_key_hash  TEXT NOT NULL DEFAULT '',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);

CREATE TABLE audit_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id  INTEGER NOT NULL,
    user_id INTEGER NOT NULL DEFAULT 0,
    action  TEXT NOT NULL,
    target  TEXT NOT NULL DEFAULT '',
    detail  TEXT NOT NULL DEFAULT '',
    ip      TEXT NOT NULL DEFAULT '',
    at      INTEGER NOT NULL
);
CREATE INDEX idx_audit_org ON audit_log (org_id, id);

-- target_groups, not "groups": GROUPS is a SQLite keyword since window frames.
CREATE TABLE target_groups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE targets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id   INTEGER NOT NULL REFERENCES target_groups(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    first_name TEXT NOT NULL DEFAULT '',
    last_name  TEXT NOT NULL DEFAULT '',
    position   TEXT NOT NULL DEFAULT '',
    custom     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_targets_group ON targets (group_id);

CREATE TABLE attachments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id       INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    content      BLOB NOT NULL,
    size         INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);

CREATE TABLE templates (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id      INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    subject     TEXT NOT NULL DEFAULT '',
    text_body   TEXT NOT NULL DEFAULT '',
    html_body   TEXT NOT NULL DEFAULT '',
    attachments TEXT NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE pages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id            INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    html              TEXT NOT NULL DEFAULT '',
    capture           INTEGER NOT NULL DEFAULT 0,
    capture_passwords INTEGER NOT NULL DEFAULT 0,
    redirect_url      TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE TABLE profiles (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id          INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    host            TEXT NOT NULL DEFAULT '',
    port            INTEGER NOT NULL DEFAULT 587,
    username        TEXT NOT NULL DEFAULT '',
    password        TEXT NOT NULL DEFAULT '',
    from_address    TEXT NOT NULL DEFAULT '',
    envelope_sender TEXT NOT NULL DEFAULT '',
    ignore_cert     INTEGER NOT NULL DEFAULT 0,
    headers         TEXT NOT NULL DEFAULT '[]',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE campaigns (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id         INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    template_id    INTEGER NOT NULL DEFAULT 0,
    page_id        INTEGER NOT NULL DEFAULT 0,
    profile_id     INTEGER NOT NULL DEFAULT 0,
    group_ids      TEXT NOT NULL DEFAULT '[]',
    url            TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'draft',
    launch_at      INTEGER NOT NULL DEFAULT 0,
    send_by        INTEGER NOT NULL DEFAULT 0,
    send_rate      INTEGER NOT NULL DEFAULT 0,
    jitter_seconds INTEGER NOT NULL DEFAULT 0,
    resend_failed  INTEGER NOT NULL DEFAULT 0,
    created_by     INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    started_at     INTEGER NOT NULL DEFAULT 0,
    completed_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_campaigns_status ON campaigns (org_id, status);

-- One row per recipient. Denormalised first-touch timestamps keep stats cheap;
-- the events table holds the full timeline.
CREATE TABLE campaign_targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    rid         TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    first_name  TEXT NOT NULL DEFAULT '',
    last_name   TEXT NOT NULL DEFAULT '',
    position    TEXT NOT NULL DEFAULT '',
    custom      TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'pending',
    error       TEXT NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    sent_at     INTEGER NOT NULL DEFAULT 0,
    opened_at   INTEGER NOT NULL DEFAULT 0,
    clicked_at  INTEGER NOT NULL DEFAULT 0,
    submitted_at INTEGER NOT NULL DEFAULT 0,
    reported_at INTEGER NOT NULL DEFAULT 0,
    submit_data TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
);
CREATE INDEX idx_ct_campaign ON campaign_targets (campaign_id);
CREATE INDEX idx_ct_email ON campaign_targets (email);

CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    rid         TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL,
    message     TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    at          INTEGER NOT NULL
);
CREATE INDEX idx_events_campaign ON events (campaign_id, id);
CREATE INDEX idx_events_rid ON events (rid);

CREATE TABLE webhooks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    url        TEXT NOT NULL,
    secret     TEXT NOT NULL DEFAULT '',
    events     TEXT NOT NULL DEFAULT '[]',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
