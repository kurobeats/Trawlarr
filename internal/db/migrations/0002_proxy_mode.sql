-- Optional adversary-in-the-middle proxy campaigns (reverse-proxy relay).
ALTER TABLE campaigns ADD COLUMN proxy_url TEXT NOT NULL DEFAULT '';
ALTER TABLE campaigns ADD COLUMN capture_cookies INTEGER NOT NULL DEFAULT 0;

CREATE TABLE proxy_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    rid         TEXT NOT NULL,
    email       TEXT NOT NULL DEFAULT '',
    upstream    TEXT NOT NULL,
    cookies     TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_proxy_sessions_rid ON proxy_sessions (rid, upstream);
