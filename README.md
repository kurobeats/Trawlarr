# Trawlarr

An open-source phishing simulation platform. Single binary, embedded UI, SQLite by default, and a migration path from Gophish databases.

Trawlarr is a from-scratch successor to [Gophish](https://github.com/gophish/gophish), not a fork. It keeps the objects people already understand — groups, templates, landing pages, sending profiles, campaigns — and rebuilds the parts that hurt in practice: the retired AngularJS front end, the untyped tracking state, the leaked product headers, and the migration story.

Use it for authorised security awareness training and red-team engagements. **You are responsible for having permission for every domain, mailbox and recipient you target.**

---

## What it does

**Campaigns**
- Immediate, scheduled, or send-by-date delivery with an optional rate limit (emails/hour) and random jitter.
- Stop, resume, and "resend failed" without duplicating recipients.
- Three delivery outcomes per recipient: `sent`, `failed`, and `uncertain` — the last one covers timeouts and resets that happen after the message may have been handed to the relay, so you never blind-resend to people who already got it.
- Full per-recipient timeline plus a campaign timeline, both immutable event rows.

**Content**
- Email templates with Gophish-compatible placeholders (`{{.FirstName}}`, `{{.URL}}`, `{{.Tracker}}`, `{{.From}}`, `{{.BaseURL}}`, …) plus `{{.OptOutURL}}` and `{{.ReportURL}}`.
- Attachments, including templated attachment filenames.
- Landing pages: hand-written HTML, or clone a live page from a URL (assets are absolutised, CSP/integrity stripped).
- Forms on a landing page are automatically repointed at the capture endpoint.
- Optional per-campaign **relay mode** (see [Relay mode](#relay-mode-aitm)).

**People and access**
- Recipient groups with CSV import (flexible headers, BOM-safe, deduplicated).
- Roles: `admin`, `operator`, `viewer`, enforced on every endpoint.
- Argon2id passwords with transparent upgrade of imported bcrypt hashes, TOTP two-factor, hashed API keys, session rotation on password change.
- Audit log for logins, object changes, launches and settings updates.

**Integrations**
- Signed webhooks (`X-Webhook-Signature: sha256=…`) for every campaign event, with retries.
- IMAP report mailbox: messages recipients report back are matched to the original recipient through the `Message-ID` (no identifying header is added to outgoing mail).
- Bearer-token REST API covering the same operations as the UI.

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

`config.json` is created on first run and rewritten when new defaults are needed. See `config.example.json`.

| Key | Meaning |
| --- | --- |
| `admin_server.listen_url` | Admin API + UI address. |
| `phish_server.listen_url` | Tracking + landing page address. Set both to the same value to serve everything from one port. |
| `phish_server.public_url` | Origin used in tracking links. Required when `listen_url` is a wildcard or behind a proxy. |
| `db_path` | SQLite file. WAL mode and a 10s busy timeout are set automatically. |
| `encryption_key` | Base64 of 32 bytes, generated on first run. Encrypts SMTP passwords, captured values and relay sessions. Changing it makes those unreadable. |
| `trusted_origins` | Extra origins accepted for state-changing requests when the admin UI sits behind a proxy. |
| `trust_proxy` | Use `X-Forwarded-For` for client IPs. Only enable behind a proxy you control. |
| `secure_cookies` | Mark the session cookie `Secure`. Set this whenever the admin UI is served over HTTPS. |
| `proxy_enabled` / `proxy_allowlist` | Enable relay mode and list the upstream hosts you are allowed to relay. Empty allowlist means nothing is allowed. |
| `imap` | Report mailbox. A database imported from Gophish supplies these too. |

---

## Relay mode (AiTM)

A campaign can either serve a static landing page or relay a live site. Relay mode is a reverse proxy with a per-recipient cookie jar: the victim talks to your domain, Trawlarr talks to the real one, and the session cookies the real site issues are captured so an engagement team can show what that session had access to.

This is the same capability as Evilginx, and it is deliberately gated:

1. `proxy_enabled` must be `true` in `config.json`.
2. The upstream host must match an entry in `proxy_allowlist`.
3. Upstream must be HTTPS.
4. Capturing cookies is a second, per-campaign opt-in.
5. Captured sessions are stored encrypted and shown only to signed-in operators.

Limits to know before you promise anyone the moon: JavaScript-heavy apps, WebSockets, service workers, and anything doing strict CORS or client-side origin checks may not relay cleanly. Relay mode is not a browser.

**Do not enable this against a site you are not contracted to test.** The allowlist exists so that a mistake in a campaign record cannot turn the instance into an open proxy.

---

## Migrating from Gophish

```sh
./trawlarr import-gophish -config config.json -from /var/lib/gophish/gophish.db
```

MySQL sources work too:

```sh
./trawlarr import-gophish -from 'mysql://user:pass@host:3306/gophish'
```

The importer reads tables by column name, so databases from roughly Gophish 0.5 onward import. It refuses to write into a non-empty destination unless you pass `-force`.

| Imported | Notes |
| --- | --- |
| Users | Bcrypt hashes keep working and are upgraded to Argon2id on first successful login. `admin`/role-slug admins map to `admin`, everyone else to `operator`. |
| API keys | Stored as hashes. Users must generate a new key. |
| Groups, targets | Deduplicated by address; extra columns become custom fields when present. |
| Templates, attachments | Attachment contents are decoded and re-encrypted. |
| Landing pages | Capture flags map across; value capture stays off. |
| Sending profiles | `host:port` is split; SMTP passwords are re-encrypted with the Trawlarr key. Headers are kept. |
| Campaigns | Statuses map across. **Nothing is auto-launched**: a campaign that was mid-send arrives as `stopped`. |
| Results and events | Milestones are rebuilt from the event stream, so funnel numbers match what Gophish showed. |
| Webhooks | Imported disabled-by-default event filter set to all. |
| IMAP settings | Imported; confirm the password in `config.json` before enabling. |

Not imported: `maillogs`, per-recipient geo coordinates, and Gophish's own session/auth tables. Gophish's `X-Gophish-*` headers are not reproduced anywhere.

See [docs/gophish-parity.md](docs/gophish-parity.md) for the issue-by-issue decisions.

---

## Sending without the usual tells

Out of the box Gophish fingerprints itself. These are fixed here rather than patched per engagement:

| Tell | Gophish | Trawlarr |
| --- | --- | --- |
| Product headers | `X-Gophish-Contact`, `X-Gophish-Signature` | none |
| `X-Mailer` | `gophish` | omitted unless the profile sets one |
| EHLO/HELO | OS hostname (set by the Ansible role, otherwise the machine name) | the `From` domain, always |
| `Message-ID` domain | derived from hostname | the `From` domain |
| `Message-ID` value | random | random, with the recipient id in the local part so reports can be matched later |
| Tracking param | `rid`, 7 alphanumeric characters (~10^12 guesses) | `rid`, 72 bits of randomness |
| 404 page | distinctive per-product page | neutral page, no product strings, no `Server` header |
| Cookies | product-named | generic names, only on the admin origin |
| Landing link | redirect chain through `/track` | one request; the page is rendered at the tracked URL |

There is no `os.Hostname()` call anywhere in the codebase; you can verify with `grep -r Hostname --include='*.go'`.

Deliverability still depends on your infrastructure: SPF, DKIM and DMARC must be configured for the sending domain, and the TLS certificate on the phishing listener must match the domain in the link.

---

## Architecture

```
cmd/trawlarr          binary, flags, import subcommand
internal/api          admin REST API, tracking endpoints, relay proxy
internal/engine       campaign runner: rendering, pacing, retries, outcomes
internal/store        typed data access over SQLite (unix-second timestamps)
internal/mail         SMTP delivery via go-mail, MIME and headers
internal/imapmon      report mailbox poller
internal/notify       signed webhook delivery with retries
internal/gophish      Gophish database importer
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
GET  /api/v1/campaigns                 list with stats
POST /api/v1/campaigns                 create
POST /api/v1/campaigns/{id}/launch
POST /api/v1/campaigns/{id}/stop
POST /api/v1/campaigns/{id}/resend
GET  /api/v1/campaigns/{id}/export     CSV results
GET  /api/v1/campaigns/{id}/targets/{tid}/session
GET|POST|PUT|DELETE /api/v1/{groups,templates,pages,profiles,attachments,webhooks}
POST /api/v1/groups/import             multipart CSV
POST /api/v1/pages/import-url          clone a live page
POST /api/v1/profiles/{id}/test        test send (result is recorded)
GET|POST|PUT|DELETE /api/v1/users      admin only
GET  /api/v1/audit
```

Public tracking endpoint (phishing listener): `/open.gif?rid=`, `/click?rid=`, `/submit?rid=` (POST), `/report?rid=`, `/optout?rid=`, and `/px/<rid>/…` for relay campaigns.

---

## Development

```sh
make test     # go test ./... (includes fuzz seed corpus)
make lint     # go vet + tsc --noEmit
make dev      # API on :3333 and Vite on :5173
```

The UI is built into `internal/web/dist` and embedded; `go build ./...` works on a fresh clone because the built bundle is committed. Run `make ui` after changing anything in `ui/`.

Useful fuzz targets:

```sh
go test -fuzz=FuzzParseTargetCSV ./internal/api
go test -fuzz=FuzzSetCookieParsing ./internal/api
```

---

## Known limits

- SQLite only. A single instance is plenty for the campaign volumes this tool is built for; there is no horizontal scaling path.
- Relay mode does not execute JavaScript, so origin checks and WebSockets can break it.
- HTML templates are rendered with `text/template` (like Gophish) so authors can write raw markup. Templates and landing pages are trusted admin input — do not hand an operator account to someone who should not be able to run arbitrary markup.
- Geo-IP enrichment, XLSX export and UI translations are not implemented. CSV export, a dashboard, and the API cover the same reporting needs.
- The webhook queue is in memory; events emitted while the process is down are not replayed.

## Prior art

Gophish is the reference implementation and the reason the objects look the way they do. [Ruphish](https://github.com/srothgan/ruphish) independently reached several of the same conclusions (privacy-first capture, uncertain delivery, Gophish migration) and is worth a look if you prefer Rust.

## License

GPL-3.0. See [LICENSE](LICENSE).
