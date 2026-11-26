# Configuration

Two places: **Settings** in the UI for anything that can change while the server runs, and `config.json` for values that need a restart or act as a safety boundary.

## In the UI

| Where | What |
| --- | --- |
| Settings → Account | Your password, TOTP enrolment, API key |
| Settings → Application | Organisation name, tracking link base URL |
| Settings → Reporting | Mailbox monitoring: enabled, host, port, username, password, TLS, ignore certificates, folder, sender-domain restriction, delete after processing, poll interval |
| Settings → AI | Enabled, base URL, model, API key, timeout |
| Settings → MCP | Enable the stdio MCP server, allow tools that change data, choose the account tool calls act as |
| Settings → Team | Users and roles |
| Settings → Audit log | Who did what |
| Webhooks | Signed event delivery per organisation |

Secrets are write-only: the API reports `has_password` / `has_api_key` and never returns the value. Leaving the field empty keeps the stored secret; a separate checkbox removes it. Reporting settings are re-read before every poll, the AI client is rebuilt on save, and MCP settings are re-read on every tool call, so no restart is needed — except that enabling MCP requires the client to relaunch the process.

The tracking base URL applies to emails sent afterwards. Campaigns can override it individually, and links already delivered keep their original host.

## Process control

**Power** in the sidebar, admin only:

| Action | Effect |
| --- | --- |
| Restart server | Re-executes the binary with the same arguments and environment, keeping the process id, so it works under Docker (PID 1), systemd and a plain shell. Campaigns that were sending resume from the database on start. |
| Shut down server | Graceful stop: listeners close, the campaign runner exits. Exit code is 0, so `Restart=on-failure` supervisors leave it down until you start it. |

Both are written to the audit log as `power.restart` / `power.shutdown`.

## In config.json

Created on first run with safe defaults. Regenerated fields (the encryption key) are written once — losing it makes stored secrets unreadable.

| Key | Meaning |
| --- | --- |
| `admin_server.listen_url` | Admin API and UI address, e.g. `127.0.0.1:3333`. |
| `admin_server.public_url` | Optional override for the admin origin. |
| `phish_server.listen_url` | Tracking and landing page address. Set the same value as the admin listener to serve everything from one port. |
| `phish_server.public_url` | Default origin for tracking links. Superseded by the UI setting. |
| `admin_server.use_tls`, `cert_path`, `key_path` | TLS for either listener. |
| `db_path` | SQLite file. WAL mode and a 10s busy timeout are applied automatically. |
| `data_dir` | Directory for runtime files. |
| `encryption_key` | Base64 of 32 bytes, generated on first run. Encrypts SMTP passwords, captured values, relay sessions, mailbox credentials and the AI key. |
| `log_level` | `debug`, `info`, `warn` or `error`. Logs go to stderr as JSON. |
| `trusted_origins` | Extra origins accepted for state-changing requests when the admin UI sits behind a proxy. |
| `trust_proxy` | Trust `X-Forwarded-For`. Only enable behind a proxy you control. |
| `secure_cookies` | Mark the session cookie `Secure`. Set this when the admin UI is served over HTTPS. |
| `session_ttl_hours` | Session lifetime, default 12. |
| `proxy_enabled` | Master switch for relay campaigns. |
| `proxy_allowlist` | Upstream hostnames relay campaigns may target. An empty list allows nothing. |
| `imap.*` | Fallback for reporting settings when the UI has none. |
| `ai.*` | Fallback for AI settings when the UI has none. |
| `mcp.enabled`, `mcp.allow_writes`, `mcp.user` | Fallback for the MCP settings when the UI has none. |

Environment overrides:

| Variable | Effect |
| --- | --- |
| `TRAWLARR_ADMIN_PASSWORD` | Password for the initial admin account on a fresh database. |
| `TRAWLARR_AI_API_KEY` | Used instead of a stored AI key. |

## Example

```json
{
  "admin_server": { "listen_url": "127.0.0.1:3333" },
  "phish_server": {
    "listen_url": "0.0.0.0:8080",
    "public_url": "https://training.example.com"
  },
  "db_path": "trawlarr.db",
  "encryption_key": "",
  "log_level": "info",
  "trust_proxy": false,
  "secure_cookies": true,
  "proxy_enabled": false,
  "proxy_allowlist": [],
  "mcp": { "enabled": false, "allow_writes": false, "user": "admin" }
}
```

A complete file with every key is in `config.example.json`.

## Reverse proxies

- Point `phish_server.public_url` (or the UI setting) at the public HTTPS origin; tracking links are built from it.
- Set `trust_proxy` and forward `X-Forwarded-For`.
- Set `secure_cookies` when the admin UI is served over HTTPS.
- Add the public admin origin to `trusted_origins` if it differs from what the server sees.
- The certificate must match the domain in the link, or recipients see a warning before anything else happens.
