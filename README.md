# Trawlarr

An open-source phishing simulation platform. Single binary, embedded UI, SQLite by default, and a migration path for existing databases.

Trawlarr covers the full simulation loop: recipient groups, email templates, landing pages, sending profiles and campaigns, with per-recipient tracking for opens, clicks, submissions and reports. It is written from scratch rather than forked from another tool, so the internals are explicit SQL, typed events and a modern UI.

Use it for authorised security awareness training and red-team engagements. **You are responsible for having permission for every domain, mailbox and recipient you target.**

---

## What it does

**Campaigns**
- Immediate, scheduled, or send-by-date delivery. A send-by date without an explicit rate spreads the audience evenly across the window; `send_rate` (emails/hour) and random jitter are available when you want direct control.
- Stop, resume, and "resend failed" without duplicating recipients.
- Three delivery outcomes per recipient: `sent`, `failed`, and `uncertain` — the last one covers timeouts and resets that happen after the message may have been handed to the relay, so you never blind-resend to people who already got it.
- Full per-recipient timeline plus a campaign timeline, both append-only event rows.
- Optional AI analysis of a finished campaign (see [AI](#ai-drafting-and-analysis)).

**Content**
- Email templates, or import an existing `.eml` and get the subject/body with every link pointed at `{{.URL}}`.
- Placeholders (`{{.FirstName}}`, `{{.URL}}`, `{{.Tracker}}`, `{{.From}}`, `{{.BaseURL}}`, …) plus `{{.OptOutURL}}` and `{{.ReportURL}}`.
- Attachments with templated contents, not just filenames: plain-text formats are rendered directly and Office documents have their XML parts rewritten.
- Landing pages: hand-written HTML, or clone a live page from a URL (assets absolutised, CSP and integrity attributes stripped).
- Forms on a landing page are automatically repointed at the capture endpoint.
- Optional per-campaign **relay mode** (see [Relay mode](#relay-mode)).

**People and access**
- Recipient groups with CSV import (flexible headers, BOM-safe, deduplicated).
- Roles: `admin`, `operator`, `viewer`, enforced on every endpoint.
- Argon2id passwords, TOTP two-factor, hashed API keys, session rotation on password change.
- Audit log for logins, object changes, launches, AI calls and settings updates.

**Integrations**
- Signed webhooks (`X-Webhook-Signature: sha256=…`) for every campaign event, with retries.
- IMAP report mailbox: reported messages are matched by scanning their body and any forwarded `.eml` attachment for the tracking parameter, plus the `Message-ID`. No identifying header is added to outgoing mail, and `imap.restrict_domain` ignores reports from outside your company.
- Bearer-token REST API covering the same operations as the UI.
- Model Context Protocol server over stdio for AI assistants (`trawlarr mcp`).

**Privacy**
- Capturing entered values is **off by default**. A submission records which fields were filled (`username`, `password`, …) and nothing else.
- Turning on value capture is per landing page and clearly labelled. Captured values live encrypted (AES-256-GCM) in the database, and passwords are dropped unless the page also enables password capture.
- `/optout` adds an address to a permanent do-not-contact list that future campaigns skip.

---

## Quick start

```sh
make build                 # builds the UI, then the Go binary into bin/trawlarr
./bin/trawlarr             # writes config.json on first run
```

The first start creates an `admin` account and prints a generated password once. Set it yourself instead:

```sh
TRAWLARR_ADMIN_PASSWORD='a long passphrase here' ./bin/trawlarr
```

Then open <http://127.0.0.1:3333>. The phishing listener runs on `0.0.0.0:8080`.

Point `phish_server.public_url` at the address recipients will actually reach before you launch anything — tracking links are built from it.

### Docker

```sh
docker build -t trawlarr .
docker run -d --name trawlarr \
  -p 127.0.0.1:3333:3333 -p 8080:8080 \
  -v trawlarr-data:/data \
  trawlarr
docker logs trawlarr | grep -A3 admin   # one-time password
```

`/data` holds `config.json` and `trawlarr.db`. Back both up together: the database is encrypted with the key in the config file.

### systemd

`deploy/trawlarr.service` is a hardened unit. Create a `trawlarr` user and put `config.json` and the database in `/var/lib/trawlarr`.

---

## Configuration

Every setting that can change at runtime is editable in **Settings** in the UI: organisation name, the tracking link
base URL, the report mailbox (host, port, credentials, TLS, folder, sender-domain restriction, delete-after-report,
poll interval) and the AI endpoint (URL, model, key, timeout). Changes apply without a restart — the mailbox poller
reloads its settings every cycle and the AI client is rebuilt on save. Secrets are write-only: the UI shows whether a
password or key is stored, never the value.

`config.json` is created on first run and holds what needs a restart or should not be switchable from a session:
listen addresses, database path, encryption key, the relay allowlist, and the MCP server. See `config.example.json`.

| Key | Meaning |
| --- | --- |
| `admin_server.listen_url` | Admin API + UI address. |
| `phish_server.listen_url` | Tracking + landing page address. Set both to the same value to serve everything from one port. |
| `phish_server.public_url` | Origin used in tracking links. Required when `listen_url` is a wildcard or behind a proxy. |
| `db_path` | SQLite file. WAL mode and a 10s busy timeout are set automatically. |
| `encryption_key` | Base64 of 32 bytes, generated on first run. Encrypts SMTP passwords, captured values, relay sessions and stored mailbox credentials. |
| `trusted_origins` | Extra origins accepted for state-changing requests when the admin UI sits behind a proxy. |
| `trust_proxy` | Use `X-Forwarded-For` for client IPs. Only enable behind a proxy you control. |
| `secure_cookies` | Mark the session cookie `Secure`. Set this whenever the admin UI is served over HTTPS. |
| `proxy_enabled` / `proxy_allowlist` | Enable relay mode and list the upstream hosts you are allowed to relay. An empty allowlist allows nothing. |
| `imap` | Report mailbox. A database imported from another instance supplies these too. |
| `ai` | Optional OpenAI-compatible endpoint for drafting and analysis. |
| `mcp` | Optional Model Context Protocol server (`trawlarr mcp`). |

---

## Relay mode

A campaign can either serve a static landing page or relay a live site. Relay mode is a reverse proxy with a per-recipient cookie jar: the victim talks to your domain, Trawlarr talks to the real one, and the session cookies the real site issues are captured so an engagement team can show what that session had access to.

This is a powerful capability, so it is deliberately gated:

1. `proxy_enabled` must be `true` in `config.json`.
2. The upstream host must match an entry in `proxy_allowlist`.
3. Upstream must be HTTPS.
4. Capturing cookies is a second, per-campaign opt-in.
5. Captured sessions are stored encrypted and shown only to signed-in operators.

Limits to know before you promise anyone the moon: JavaScript-heavy apps, WebSockets, service workers, and anything doing strict CORS or client-side origin checks may not relay cleanly. Relay mode is not a browser.

**Do not enable this against a site you are not contracted to test.** The allowlist exists so that a mistake in a campaign record cannot turn the instance into an open proxy.

---

## Importing an existing database

```sh
./trawlarr import-gophish -config config.json -from /var/lib/gophish/gophish.db
```

MySQL sources work too:

```sh
./trawlarr import-gophish -from 'mysql://user:pass@host:3306/gophish'
```

The importer reads tables by column name, so databases created by any release from roughly 0.5 onward import. It refuses to write into a non-empty destination unless you pass `-force`.

| Imported | Notes |
| --- | --- |
| Users | Bcrypt hashes keep working and are upgraded to Argon2id on first successful login. `admin`/role-slug admins map to `admin`, everyone else to `operator`. |
| API keys | Stored as hashes. Users must generate a new key. |
| Groups, targets | Deduplicated by address; extra columns become custom fields when present. |
| Templates, attachments | Attachment contents are decoded and re-encrypted. |
| Landing pages | Capture flags map across; value capture stays off. |
| Sending profiles | `host:port` is split; SMTP passwords are re-encrypted with the Trawlarr key. Headers are kept. |
| Campaigns | Statuses map across. **Nothing is auto-launched**: a campaign that was mid-send arrives as `stopped`. |
| Results and events | Milestones are rebuilt from the event stream, so funnel numbers match the source. |
| Webhooks | Imported disabled-by-default event filter set to all. |
| IMAP settings | Imported; confirm the password before enabling the monitor. |

Not imported: mail logs, per-recipient geo coordinates, and session tables.

See [docs/design-rationale.md](docs/design-rationale.md) for the reasoning behind individual behaviours.

---

## Sending without the usual tells

Several details commonly leak which tool sent an email or which tool serves a landing page. These are fixed in the code, not patched per engagement:

| Problem | What Trawlarr does |
| --- | --- |
| Vendor headers in mail | none at all |
| `X-Mailer` naming the tool | omitted unless the sending profile sets one |
| EHLO/HELO sending the OS hostname | the `From` domain, always |
| `Message-ID` built from the hostname | the `From` domain |
| `Message-ID` revealing nothing useful | random, with the recipient id in the local part so reported messages can be matched later |
| Short, enumerable recipient ids | 72 bits of randomness |
| Error pages that identify the software | neutral page, no product strings, no `Server` header |
| Product-named cookies | generic names, only on the admin origin |
| A redirect hop before the landing page | one request; the page is rendered at the tracked URL |

There is no `os.Hostname()` call anywhere in the codebase; you can verify with `grep -r Hostname --include='*.go'`.

Deliverability still depends on your infrastructure: SPF, DKIM and DMARC must be configured for the sending domain, and the TLS certificate on the phishing listener must match the domain in the link.

---

## AI drafting and analysis

Any endpoint that implements `POST /chat/completions` works — hosted APIs, Ollama, vLLM, LM Studio, a gateway in front of several providers.

```json
"ai": {
  "enabled": true,
  "base_url": "http://127.0.0.1:11434/v1",
  "model": "llama3.1:8b",
  "api_key": "",
  "timeout_seconds": 45
}
```

With it enabled:

- **Draft an email or landing page** from a short brief, in the template and page editors or through the API.
- **Analyse a campaign** on the campaign page: a short risk assessment with a recommended next action.
- The same tools are available to MCP clients.

Analysis only ever sends aggregate numbers — recipient counts and event totals. Drafting sends your brief. No recipient addresses or captured values leave the process.

The API key can also come from `TRAWLARR_AI_API_KEY`, which keeps it out of the config file.

---

## MCP server

`trawlarr mcp` speaks the Model Context Protocol over **stdio**, so an assistant launches it as a child process. There is no network listener, no extra port and no unauthenticated endpoint to forget about.

```json
"mcp": {
  "enabled": true,
  "allow_writes": false,
  "user": "admin"
}
```

- `user` selects the account tool calls act as; the first admin is used when empty.
- `allow_writes` gates `create_*`, `launch_campaign` and `stop_campaign`. Read tools always work.
- Every call is written to the audit log as `mcp.<tool>`.

Example client entry:

```json
{
  "mcpServers": {
    "trawlarr": {
      "command": "/usr/local/bin/trawlarr",
      "args": ["mcp", "-config", "/var/lib/trawlarr/config.json"]
    }
  }
}
```

Tools: `dashboard`, `list_campaigns`, `get_campaign`, `export_campaign`, `analyze_campaign`, `list_groups`, `list_templates`, `get_template`, `list_landing_pages`, `list_sending_profiles`, `draft_email`, `draft_landing_page`, `create_group`, `create_template`, `create_landing_page`, `create_campaign`, `launch_campaign`, `stop_campaign`.

`launch_campaign` queues the campaign rather than sending from the MCP process; the running server picks it up within a couple of seconds. Clients should send an `arguments` object (empty is fine) with every tool call.

---

## Architecture

```
cmd/trawlarr          binary, flags, import and mcp subcommands
internal/api          admin REST API, tracking endpoints, relay proxy
internal/engine       campaign runner: rendering, pacing, retries, outcomes
internal/store        typed data access over SQLite (unix-second timestamps)
internal/mail         SMTP delivery, MIME and headers
internal/ai           OpenAI-compatible drafting and analysis
internal/mcp          Model Context Protocol tools over stdio
internal/imapmon      report mailbox poller
internal/notify       signed webhook delivery with retries
internal/gophish      database importer
internal/web          embedded UI bundle
ui/                   React + TypeScript admin interface
```

- One SQLite connection pool, WAL mode, `busy_timeout=10000`. Writes are short transactions; the campaign runner does not hold a connection while sending mail.
- Timestamps are stored as UTC unix seconds and rendered in the browser's time zone, so there is no server/browser disagreement.
- Migrations are embedded SQL files applied in order and recorded in `schema_migrations`.

---

## API

All admin endpoints live under `/api/v1` and accept either the session cookie or `Authorization: Bearer <api key>`. Errors are `{"error": "..."}` with a matching status code.

```
POST /api/v1/login                     {username, password, totp?}
GET  /api/v1/me
GET  /api/v1/dashboard
POST /api/v1/ai/generate               {kind: email|landing_page, prompt}
GET  /api/v1/ai/status
GET  /api/v1/campaigns                 list with stats
POST /api/v1/campaigns                 create
POST /api/v1/campaigns/{id}/launch
POST /api/v1/campaigns/{id}/stop
POST /api/v1/campaigns/{id}/resend
POST /api/v1/campaigns/{id}/analyze    AI risk assessment
GET  /api/v1/campaigns/{id}/export     CSV results
GET  /api/v1/campaigns/{id}/targets/{tid}/session
GET|POST|PUT|DELETE /api/v1/{groups,templates,pages,profiles,attachments,webhooks}
POST /api/v1/groups/import             multipart CSV
POST /api/v1/pages/import-url          clone a live page
POST /api/v1/profiles/{id}/test        test send (result is recorded)
GET|POST|PUT|DELETE /api/v1/users      admin only
GET  /api/v1/audit
```

Public tracking endpoints (phishing listener): `/open.gif?rid=`, `/click?rid=`, `/submit?rid=` (POST), `/report?rid=`, `/optout?rid=`, and `/px/<rid>/…` for relay campaigns.

---

## Development

```sh
make test     # go test -race -count=1 ./...   (full suite, race detector)
make test-short
make cover    # coverage summary + coverage.html
make lint     # go vet + tsc --noEmit
make dev      # API on :3333 and Vite on :5173
```

The suite covers every package, including HTTP-level tests against a real server instance, a stdio MCP session driven over pipes, an in-process SMTP sink that inspects the bytes on the wire, an importer-shaped fixture database, and process-level tests that build and run the binary (startup, single-port mode, graceful shutdown). CI runs `go vet`, the race-enabled suite, `tsc --noEmit`, the UI build and gosec.

The UI is built into `internal/web/dist` and embedded; `go build ./...` works on a fresh clone because the built bundle is committed. Run `make ui` after changing anything in `ui/`.

Fuzz targets:

```sh
go test -fuzz=FuzzParseTargetCSV ./internal/api
go test -fuzz=FuzzSetCookieParsing ./internal/api
go test -fuzz=FuzzRewriteLinks ./internal/api
```

---

## Known limits

- SQLite only. A single instance is plenty for the campaign volumes this tool is built for; there is no horizontal scaling path.
- Relay mode does not execute JavaScript, so origin checks and WebSockets can break it.
- HTML templates are rendered with `text/template` so authors can write raw markup. Templates and landing pages are trusted admin input — do not hand an operator account to someone who should not be able to run arbitrary markup.
- Geo-IP enrichment and UI translations are not implemented. CSV export, the dashboard, the API and MCP cover reporting needs.
- The webhook queue is in memory; events emitted while the process is down are not replayed.
- The MCP server is stdio-only by design. Exposing it over the network is left to an MCP gateway you control.

## License

GPL-3.0. See [LICENSE](LICENSE).
