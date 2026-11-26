-- Certificate profiles: one per set of domains, selected per campaign.
CREATE TABLE certificates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    domains    TEXT NOT NULL DEFAULT '[]',
    email      TEXT NOT NULL DEFAULT '',
    staging    INTEGER NOT NULL DEFAULT 0,
    accept_tos INTEGER NOT NULL DEFAULT 0,
    cache_dir  TEXT NOT NULL DEFAULT '',
    http_addr  TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

ALTER TABLE campaigns ADD COLUMN certificate_id INTEGER NOT NULL DEFAULT 0;
