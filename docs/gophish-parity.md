# Gophish parity, issue by issue

Trawlarr was built by reading Gophish's tracker, not by copying its code. This is the decision log: what a reported problem was, and what Trawlarr does instead. Issue numbers refer to `gophish/gophish`.

## Delivered

| Issue | Problem | Trawlarr |
| --- | --- | --- |
| #1602, #1929, PR #1642 | No built-in webhooks; custom events wanted | Webhook objects with per-event subscriptions, HMAC-SHA256 signatures, retries, and fired for every recipient event. |
| #976, #1226, #1782, PR #1612, #989 | Recipients cannot report phish | `/report?rid=` endpoint, `{{.ReportURL}}` placeholder, IMAP report mailbox that matches reported messages back to the recipient through `Message-ID`. |
| #1553 | `X-Mailer: gophish` leaks the tool | Header omitted unless the sending profile sets one. |
| — (sneaky_gophish) | `X-Gophish-Contact` / `X-Gophish-Signature` | No product headers anywhere in mail or webhooks. |
| — (SMTP hostname leak) | HELO/EHLO sends the OS hostname | HELO, TLS SNI and `Message-ID` all use the `From` domain. No `os.Hostname()` call exists. |
| #227, #2585, #2437, PR #260 | Attachment open tracking | Not implemented as tracking. Attachments are templated; tracking pixels only cover the body. |
| PR #1936 | Templated attachment contents | Attachment filenames are templated per recipient. |
| #767, #1171, #152 | CSV import failures, Unicode, BOM | Header names are matched loosely (`First Name` → `first_name`), BOM stripped, duplicates and invalid addresses dropped, unknown columns kept as custom fields. |
| #1333, #1034 | RBAC is one bit (`admin`/`user`) | Three roles enforced per route, last-admin protection, per-user API keys. |
| #1538 | Password policy is "8 characters" | Argon2id, 12-character minimum, TOTP 2FA, login throttling, session rotation. |
| #316 | Browser/server time zone disagreement | All timestamps stored as UTC unix seconds, rendered locally. |
| #340 | `database is locked` on SQLite | WAL, 10s `busy_timeout`, no connection held across network calls. |
| #2107, #2765, PR #2301 | CSRF and `referer invalid` behind proxies | SameSite=Lax session cookie plus an Origin/Referer check against the request host and `trusted_origins`. No CSRF token round-trip to lose. |
| #1999, #2624 | Wrong client IP behind a proxy; bad sender syntax | Opt-in `trust_proxy`; envelope sender parsed and validated separately from the header `From`. |
| #858, PR #986 | Cannot set envelope sender separately | `EnvelopeSender` on the sending profile. |
| #611, #501, PR #391, PR #482 | No throttling or send pacing | `send_rate` (emails/hour) and `jitter_seconds` per campaign. |
| #79 | Cannot resend failed messages | `POST /campaigns/{id}/resend` requeues failed and uncertain recipients only. |
| #549 | Copying a campaign loses fields | Campaign definitions are plain records; the UI edits them in place rather than recreating them. |
| #265, #1921, #1253 | Form capture fails or silently drops data | Forms are rewritten to the capture endpoint; field names are always recorded; values are optional and encrypted. |
| #2171, #622 | Redirect page served instead of landing page | The landing page is rendered for GET; only a successful POST redirects. |
| #220, #212, #1930, PR #200 | Imported pages lose images; proxy issues | URL import absolutises `src`/`href`/`action` against the final redirect URL, strips `integrity`/CORS and CSP meta. |
| #1915 | False-positive submissions | Captured events are explicit per recipient; campaign stats are derived from those rows rather than a mutable status string. |
| #1982 | No health check | `GET /api/v1/health` (admin) and `/healthz` (phishing listener). |
| #586, #2285 | No service management | Hardened systemd unit and a distroless container; no host daemonising. |
| #518 | Recipient ids are short enough to enumerate | 72-bit random recipient ids instead of 7 alphanumerics. |
| #536 | Same address in two campaigns | Recipients are keyed per campaign, not globally. |
| #2679 | Proxy prefetch inflates "opened" | Only the first open per recipient is recorded. Click and open are separate requests with no redirect chain. |
| #2018, #1492, #1065 | Summary numbers disagree with timelines | Stats are SQL aggregates over `campaign_targets`; the timeline is append-only events. They cannot drift. |
| #1979 | Listen URL vs campaign URL confusion | `phish_server.public_url`, plus an optional per-campaign URL override. |
| #2443 | Angle brackets break the From field | From addresses are parsed with `net/mail`, not string splitting. |
| #2637, #2261 | Unicode attachment names | MIME encoding handled by go-mail; filenames are sanitised to a basename. |
| #1843 | QR codes for mobile | Not implemented. `{{.URL}}` can be placed in any QR generator. |
| #349 | Multiple TLS landing pages | One listener, per-campaign relay or landing page. Terminate TLS per hostname in a proxy. |

## Deliberate differences

| Topic | Gophish | Trawlarr |
| --- | --- | --- |
| Captured credentials | Stored in clear text, passwords optional | Field names always, values only on explicit opt-in, always encrypted at rest |
| API keys | Stored in clear text | SHA-256 hashed, shown once |
| Passwords | bcrypt | Argon2id; bcrypt accepted and upgraded on login |
| Campaign status | Free-form strings, several active states | `draft`, `scheduled`, `queued`, `sending`, `completed`, `stopped`, `error` |
| Failed delivery | `Error` | `failed` vs `uncertain`, because a timeout is not proof of non-delivery |
| Frontend | AngularJS 1.x | React + TypeScript, embedded in the binary |
| Storage | SQLite or MySQL via GORM | SQLite with explicit SQL and embedded migrations |

## Not carried over

- Geo-IP lookups on open/click. They are optional in Gophish, require a MaxMind database, and add a third-party dependency for data most programmes do not use.
- `maillogs` and `email_requests`. Useful for Gophish's IMAP flow; superseded by events plus `Message-ID` matching.
- Mail-tester and other online helper integrations.
- The `X-Gophish-*` transparency headers.
