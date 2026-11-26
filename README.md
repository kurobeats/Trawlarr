<p align="center">
  <img src="assets/trawlarr.png" alt="Trawlarr" width="200">
</p>

<h1 align="center">Trawlarr</h1>

<p align="center">Open-source phishing simulation platform.</p>

One binary with the UI embedded, SQLite storage, and an importer for existing databases. Groups, templates, landing pages, sending profiles and campaigns, with per-recipient tracking of opens, clicks, submissions and reports. Written from scratch, so the internals are explicit SQL, typed events and a modern interface.

Use it for authorised security-awareness training and red-team work. **You are responsible for having permission for every domain, mailbox and recipient you target.**

## Quick start

```sh
make build          # UI, then the binary in bin/trawlarr
./bin/trawlarr      # creates config.json on first run
```

First start creates `admin` and prints a password once. Supply your own instead:

```sh
TRAWLARR_ADMIN_PASSWORD='a long passphrase' ./bin/trawlarr
```

Open <http://127.0.0.1:3333>; recipients are served from `0.0.0.0:8080`. Set the tracking URL in **Settings → Application** before launching anything.

Docker:

```sh
docker build -t trawlarr .
docker run -d --name trawlarr -p 127.0.0.1:3333:3333 -p 8080:8080 \
  -v trawlarr-data:/data trawlarr
docker logs trawlarr | grep -A3 admin    # one-time password
```

`/data` holds `config.json` and `trawlarr.db`; back them up together. `deploy/trawlarr.service` is a hardened systemd unit.

## What it does

**Campaigns** — launch now, schedule, or spread delivery to a send-by date. Rate limit and jitter when you want direct control. Stop, resume, resend only the failures. Each recipient ends up `sent`, `failed` or `uncertain`; the last one means the SMTP session broke after the message may have been handed over, so a timeout never triggers a blind resend.

**Content** — templates with the usual placeholders (`{{.FirstName}}`, `{{.URL}}`, `{{.Tracker}}`, `{{.BaseURL}}`, `{{.OptOutURL}}`, `{{.ReportURL}}`), or import an existing `.eml` with every link pointed at `{{.URL}}`. Attachments are templated inside as well as in the filename, including Office documents. Landing pages can be written or cloned from a live URL; forms are repointed at the capture endpoint automatically.

**Access** — `admin`, `operator` and `viewer` roles enforced on every endpoint; Argon2id passwords, TOTP, hashed API keys, session rotation, audit log.

**Integrations** — signed webhooks for every event, an IMAP report mailbox, a REST API, and a Model Context Protocol server for AI assistants.

**Privacy** — a submission records *which* fields were filled, not their contents. Storing values is per-page, opt-in, and encrypted; passwords need a second opt-in. `/optout` keeps an address out of future campaigns.

## Settings

Everything that can change at runtime lives in **Settings**: organisation name, tracking URL, report mailbox (host, port, credentials, TLS, folder, sender-domain restriction, delete-after-processing, poll interval) and the AI endpoint. Changes apply without a restart; secrets are write-only and the UI only reports whether one is stored.

`config.json` holds what needs a restart or is a safety boundary: listen addresses, database path, encryption key, relay allowlist and the MCP server. Full reference: [docs/configuration.md](docs/configuration.md).

## Fingerprints

Commonly leaked details are fixed in the code, not patched per engagement:

| Leak | Here |
| --- | --- |
| Vendor headers, `X-Mailer`, `User-Agent` | absent |
| EHLO/HELO and `Message-ID` built from the hostname | the `From` domain, always |
| Enumerable recipient ids | 72 bits of randomness |
| Error pages, cookies or redirect hops that name the software | neutral page, generic cookie names, page served at the tracked URL |

There is no `os.Hostname()` call in the codebase. Deliverability still depends on your SPF, DKIM and DMARC records and a certificate matching the link domain.

## Relay mode

A campaign can relay a live site instead of serving a landing page: a reverse proxy with a per-recipient cookie jar that captures the session cookies the real site issues.

It is deliberately gated — `proxy_enabled` in the config, an upstream host in `proxy_allowlist`, HTTPS only, and cookie capture as a second per-campaign opt-in. JavaScript-heavy apps, WebSockets and strict client-side origin checks may not relay cleanly. **Do not enable it against a site you are not contracted to test.**

## Importing a database

```sh
./trawlarr import-gophish -config config.json -from /var/lib/gophish/gophish.db
./trawlarr import-gophish -from 'mysql://user:pass@host:3306/gophish'
```

Reads by column name, so older schemas work. Refuses to write into a non-empty destination without `-force`. Users keep their bcrypt passwords (upgraded on first login); API keys become hashes and must be regenerated; campaigns arrive with their statuses but **nothing is auto-launched** — a campaign that was mid-send arrives as `stopped`.

## AI and MCP

`Settings → AI` points at any OpenAI-compatible endpoint (hosted API, Ollama, vLLM, LM Studio, a gateway). It drafts emails and landing pages from a brief and analyses a finished campaign; analysis sends aggregate numbers only. `TRAWLARR_AI_API_KEY` works instead of storing the key.

`trawlarr mcp` is a stdio MCP server with 18 tools for read access, drafting and campaign management. Writes stay off until `mcp.allow_writes` is set, every call is audited, and `launch_campaign` queues the campaign for the running server rather than sending from the MCP process.

## API

Admin endpoints under `/api/v1` accept the session cookie or `Authorization: Bearer <key>`; errors are `{"error": "..."}`.

```
POST /login  GET|POST|PUT|DELETE /{groups,templates,pages,profiles,webhooks}
GET  /dashboard   GET|PUT /settings   GET /audit
GET|POST /campaigns        POST /campaigns/{id}/{launch,stop,resend,analyze}
GET  /campaigns/{id}/export | /targets/{tid}/session
POST /ai/generate   GET /ai/status   POST /groups/import
POST /pages/import-url   POST /templates/import-email   POST /profiles/{id}/test
GET|POST|PUT|DELETE /users    (admin)
```

Tracking endpoints on the phishing listener: `/open.gif?rid=`, `/click?rid=`, `/submit?rid=` (POST), `/report?rid=`, `/optout?rid=`, `/px/<rid>/…`.

## Development

```sh
make test     # go test -race -count=1 ./...
make cover    # coverage summary + coverage.html
make lint     # go vet + tsc --noEmit
make dev      # API on :3333, Vite on :5173
```

The suite covers every package: HTTP tests against real handlers, a stdio MCP session over pipes, an in-process SMTP sink that inspects the bytes on the wire, an importer fixture database, and process-level tests that build and run the binary. Fuzz targets cover CSV parsing, cookie parsing and link rewriting.

The UI bundle in `internal/web/dist` is committed so a fresh clone builds; run `make ui` after changing `ui/`.

## Limits

- SQLite only, single instance. No horizontal scaling.
- Relay mode is not a browser: no JavaScript, so SPA logins can break.
- Templates render with `text/template` for raw markup; operator accounts are trusted.
- Webhook delivery is queued in memory, so events during downtime are lost.
- No Geo-IP, no UI translations, no XLSX export.

Design decisions and the behaviour-by-behaviour rationale: [docs/design-rationale.md](docs/design-rationale.md).

## License

GPL-3.0. See [LICENSE](LICENSE).
