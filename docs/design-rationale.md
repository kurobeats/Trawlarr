# Design rationale

Trawlarr was designed by reading the public issue tracker of the tool it replaces, then deciding each behaviour deliberately. This is the decision log. Issue numbers refer to the upstream project.

## Behaviours and why

| Upstream issue | Problem | Decision |
| --- | --- | --- |
| #1602, #1929 | No built-in webhooks; custom events wanted | Webhook objects with per-event subscriptions, HMAC-SHA256 signatures, retries, fired for every recipient event. |
| #976, #1226, #1782 | Recipients cannot report phish | `/report?rid=` endpoint, `{{.ReportURL}}` placeholder, and an IMAP report mailbox. Reported messages are matched by scanning the body and any forwarded `.eml` attachment for the tracking parameter, with `Message-ID` as a second signal; an optional domain restriction ignores outside senders. |
| #1553 | `X-Mailer` names the tool | Header omitted unless the sending profile sets one. |
| — | Vendor-specific contact and signature headers | No product headers in mail or webhooks. |
| — | HELO/EHLO sends the machine hostname | HELO, TLS SNI and `Message-ID` all use the `From` domain. There is no `os.Hostname()` call in the codebase. |
| #227, #2585, #2437 | Attachment open tracking | Not implemented as tracking: attachments are templated, and pixels cover the body only. |
| #1936 | Templated attachment names | Attachment filenames render per recipient. |
| #767, #1171, #152 | CSV import failures, Unicode, BOM | Loose header matching (`First Name` → `first_name`), BOM stripped, duplicates and invalid addresses dropped, unknown columns kept as custom fields. |
| #1333, #1034 | Roles are a single admin bit | Three roles enforced per route, last-admin protection, per-user API keys. |
| #1538 | Weak password policy | Argon2id, 12-character minimum, TOTP, login throttling, session rotation. |
| #316 | Browser/server time zone disagreement | UTC unix seconds in the database, rendered locally. |
| #340 | `database is locked` | WAL, 10s busy timeout, no connection held across network calls. |
| #2107, #2765 | CSRF failures behind proxies | SameSite=Lax cookie plus an Origin/Referer check against the request host and `trusted_origins`. No token round-trip to lose. |
| #1999, #2624 | Wrong client IP behind a proxy; invalid sender syntax | Opt-in `trust_proxy`; envelope sender parsed separately from the header `From`. |
| #858, #986 | Cannot set the envelope sender separately | `EnvelopeSender` on the sending profile. |
| #611, #501, #391, #482 | No throttling or pacing | `send_rate` (emails/hour) and `jitter_seconds` per campaign. |
| #79 | Cannot resend failed messages | `POST /campaigns/{id}/resend` requeues failed and uncertain recipients only. |
| send-by scheduling | A send-by date only set a deadline | The remaining audience is spread evenly across the remaining window unless an explicit rate is configured. |
| — | No way to reuse an existing email | `POST /api/v1/templates/import-email` parses a raw `.eml`, decodes multipart, quoted-printable, base64 and non-UTF-8 charsets, and can point every link at the tracked URL. |
| #767 | CSV columns are guessed by content patterns | A header row is matched by name; without one the email column is located by its values, so reordered exports import correctly. |
| #549 | Copying a campaign loses fields | Campaigns are records edited in place instead of being recreated from a form. |
| #265, #1921, #1253 | Form capture fails silently | Forms are rewritten to the capture endpoint; field names are always recorded; values are optional and encrypted. |
| #2171, #622 | Redirect page served instead of the landing page | Landing page on GET; redirect only after a successful POST. |
| #220, #212, #1930 | Imported pages lose images | URL import absolutises `src`/`href`/`action` against the final redirect URL and strips `integrity`, CORS and CSP meta. |
| #1915 | False-positive submissions | Engagement is explicit event rows; stats are aggregates over recipient state, not a mutable status string. |
| #1982 | No health check | `GET /api/v1/health` and `/healthz`. |
| #586, #2285 | No service management | Hardened systemd unit and a distroless container. |
| #518 | Enumerable recipient ids | 72-bit random recipient ids. |
| #536 | Same address in two campaigns | Recipients are keyed per campaign. |
| #2679 | Prefetching inflates "opened" | Only the first open per recipient is recorded; click and open are separate requests with no redirect chain. |
| #2018, #1492, #1065 | Summary numbers disagree with timelines | Stats are SQL aggregates over `campaign_targets`; the timeline is append-only events. They cannot drift. |
| #1979 | Listen URL and campaign URL confusion | `phish_server.public_url` plus an optional per-campaign override. |
| #2443 | Angle brackets break the From field | Addresses parsed with `net/mail`. |
| #2637, #2261 | Unicode attachment names | MIME encoding by the mail library; filenames reduced to a safe basename. |
| #1843 | QR codes for mobile | Not implemented; `{{.URL}}` can be placed in any QR generator. |
| #349 | Multiple TLS landing pages | One listener; terminate TLS per hostname in a proxy. |

## Deliberate differences

| Topic | Typical implementation | Trawlarr |
| --- | --- | --- |
| Captured credentials | Stored in clear text, passwords optional | Field names always, values only on explicit opt-in, always encrypted at rest |
| API keys | Stored in clear text | SHA-256 hashed, shown once |
| Passwords | bcrypt | Argon2id; bcrypt accepted and upgraded on login |
| Campaign status | Free-form strings and several active states | `draft`, `scheduled`, `queued`, `sending`, `completed`, `stopped`, `error` |
| Failed delivery | One "error" state | `failed` vs `uncertain`, because a timeout is not proof of non-delivery |
| Frontend | AngularJS 1.x | React + TypeScript, embedded in the binary |
| Storage | SQLite or MySQL through an ORM | SQLite with explicit SQL and embedded migrations |

## Not carried over

- Geo-IP lookups on open and click. Optional upstream, requires a MaxMind database, and few programmes use the data.
- Mail logs and email-request records. Superseded by events plus `Message-ID` matching.
- Online mail-tester integrations.
- Transparency headers that name the tool.
