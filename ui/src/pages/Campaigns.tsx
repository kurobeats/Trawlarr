import { useMemo, useState } from 'react'
import { api, errorText, pct, timeAgo } from '../api'
import { useAIStatus } from '../ai'
import { Badge, ErrorBox, Field, Loading, Metric, Modal, useAsync } from '../ui'

type Stats = {
  total: number
  pending: number
  sent: number
  opened: number
  clicked: number
  submitted: number
  reported: number
  failed: number
  opted_out: number
  uncertain: number
}

type Campaign = {
  id: number
  name: string
  status: string
  template_id: number
  page_id: number
  profile_id: number
  group_ids: number[]
  url: string
  proxy_url: string
  capture_cookies: boolean
  launch_at: string | null
  send_by: string | null
  send_rate: number
  jitter_seconds: number
  created_at: string
  stats: Stats
  results?: Target[]
  timeline?: Event[]
}

type Target = {
  id: number
  has_session: boolean
  email: string
  first_name: string
  last_name: string
  position: string
  status: string
  error: string
  sent_at: string | null
  opened_at: string | null
  clicked_at: string | null
  submitted_at: string | null
  reported_at: string | null
  data?: Record<string, unknown>
  fields?: string[]
}

type Event = { id: number; type: string; message: string; email: string; at: string; ip: string }

type Ref = { id: number; name: string }
type Group = Ref & { targets: unknown[] }

export function Campaigns({ navigate }: { navigate: (p: string) => void }) {
  const list = useAsync(() => api.get<Campaign[]>('/api/v1/campaigns'))
  const [creating, setCreating] = useState(false)

  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Campaigns</h1>
        <div className="grow" />
        <button className="primary" onClick={() => setCreating(true)}>
          New campaign
        </button>
      </div>
      <ErrorBox message={list.error} />
      {list.loading ? (
        <Loading />
      ) : (
        <div className="card">
          {!list.data?.length ? (
            <p className="muted">No campaigns yet.</p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Progress</th>
                  <th>Opened</th>
                  <th>Clicked</th>
                  <th>Submitted</th>
                  <th>Reported</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {list.data.map((c) => {
                  const done = c.stats.sent + c.stats.failed
                  return (
                    <tr key={c.id}>
                      <td>
                        <a href={`#/campaigns/${c.id}`}>{c.name}</a>
                      </td>
                      <td>
                        <Badge status={c.status} />
                      </td>
                      <td style={{ minWidth: 120 }}>
                        <div className="bar">
                          <span style={{ width: pct(done, c.stats.total) }} />
                        </div>
                        <span className="muted small">
                          {done}/{c.stats.total}
                        </span>
                      </td>
                      <td>{pct(c.stats.opened, c.stats.sent)}</td>
                      <td>{pct(c.stats.clicked, c.stats.sent)}</td>
                      <td>{pct(c.stats.submitted, c.stats.sent)}</td>
                      <td>{pct(c.stats.reported, c.stats.sent)}</td>
                      <td className="muted small">{timeAgo(c.created_at)}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      )}
      {creating && (
        <CampaignForm
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            navigate(`/campaigns/${c.id}`)
          }}
        />
      )}
    </>
  )
}

function CampaignForm({ onClose, onSaved }: { onClose: () => void; onSaved: (c: Campaign) => void }) {
  const templates = useAsync(() => api.get<Ref[]>('/api/v1/templates'))
  const pages = useAsync(() => api.get<Ref[]>('/api/v1/pages'))
  const profiles = useAsync(() => api.get<Ref[]>('/api/v1/profiles'))
  const groups = useAsync(() => api.get<Group[]>('/api/v1/groups'))
  const certs = useAsync(() => api.get<{ id: number; name: string; domains: string[] }[]>('/api/v1/certificates'))

  const [name, setName] = useState('')
  const [templateId, setTemplateId] = useState(0)
  const [pageId, setPageId] = useState(0)
  const [profileId, setProfileId] = useState(0)
  const [groupId, setGroupId] = useState(0)
  const [url, setUrl] = useState('')
  const [relay, setRelay] = useState(false)
  const [proxyUrl, setProxyUrl] = useState('')
  const [captureCookies, setCaptureCookies] = useState(false)
  const [certId, setCertId] = useState(0)
  const [launchAt, setLaunchAt] = useState('')
  const [sendBy, setSendBy] = useState('')
  const [rate, setRate] = useState(0)
  const [jitter, setJitter] = useState(0)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const ready = templates.data?.length && pages.data?.length && profiles.data?.length && groups.data?.length

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const body = {
        name,
        template_id: templateId,
        page_id: pageId,
        profile_id: profileId,
        group_ids: [groupId],
        url,
        proxy_url: relay ? proxyUrl : '',
        capture_cookies: relay && captureCookies,
        certificate_id: certId,
        launch_at: launchAt ? new Date(launchAt).toISOString() : null,
        send_by: sendBy ? new Date(sendBy).toISOString() : null,
        send_rate: rate,
        jitter_seconds: jitter,
      }
      const c = await api.post<Campaign>('/api/v1/campaigns', body)
      onSaved(c)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="New campaign" onClose={onClose}>
      {!ready ? (
        <p className="muted">
          A campaign needs at least one template, landing page, sending profile and recipient group. Create those
          first under the library pages.
        </p>
      ) : (
        <form onSubmit={save}>
          <ErrorBox message={error} />
          <Field label="Campaign name">
            <input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
          </Field>
          <div className="row">
            <div style={{ flex: 1 }}>
              <Field label="Email template">
                <Select items={templates.data!} value={templateId} onChange={setTemplateId} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Landing page">
                <Select items={pages.data!} value={pageId} onChange={setPageId} />
              </Field>
            </div>
          </div>
          <div className="row">
            <div style={{ flex: 1 }}>
              <Field label="Sending profile">
                <Select items={profiles.data!} value={profileId} onChange={setProfileId} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Recipient group">
                <Select items={groups.data!} value={groupId} onChange={setGroupId} />
              </Field>
            </div>
          </div>
          <Field
            label="Tracking URL base"
            hint="Leave blank to use the phishing server URL from config. Override per campaign for a dedicated domain."
          >
            <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://login.example.com" />
          </Field>
          <Field label="Landing page behaviour">
            <label className="chip">
              <input
                type="checkbox"
                style={{ width: 'auto' }}
                checked={relay}
                onChange={(e) => setRelay(e.target.checked)}
              />
              Relay a live site instead of using the landing page
            </label>
          </Field>
          {relay && (
            <div className="card" style={{ background: 'var(--panel-2)' }}>
              <Field
                label="Upstream URL"
                hint="Must be https and listed in proxy_allowlist in config.json. Only use this against sites you are authorised to test."
              >
                <input
                  value={proxyUrl}
                  onChange={(e) => setProxyUrl(e.target.value)}
                  placeholder="https://login.example.com"
                />
              </Field>
              <label className="chip">
                <input
                  type="checkbox"
                  style={{ width: 'auto' }}
                  checked={captureCookies}
                  onChange={(e) => setCaptureCookies(e.target.checked)}
                />
                Capture upstream session cookies
              </label>
            </div>
          )}
          <div className="row">
            <div style={{ flex: 1 }}>
              <Field label="Launch at" hint="Leave blank to start immediately when launched.">
                <input type="datetime-local" value={launchAt} onChange={(e) => setLaunchAt(e.target.value)} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Stop sending after">
                <input type="datetime-local" value={sendBy} onChange={(e) => setSendBy(e.target.value)} />
              </Field>
            </div>
          </div>
          <div className="row">
            <div style={{ flex: 1 }}>
              <Field label="Send rate (emails/hour)" hint="0 sends as fast as the relay allows.">
                <input type="number" min={0} value={rate} onChange={(e) => setRate(Number(e.target.value))} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Random delay (seconds)" hint="Added to each gap between emails.">
                <input type="number" min={0} value={jitter} onChange={(e) => setJitter(Number(e.target.value))} />
              </Field>
            </div>
          </div>
          <div className="row" style={{ justifyContent: 'flex-end' }}>
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button className="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : 'Create campaign'}
            </button>
          </div>
        </form>
      )}
    </Modal>
  )
}

function Select({
  items,
  value,
  onChange,
}: {
  items: Ref[]
  value: number
  onChange: (n: number) => void
}) {
  return (
    <select value={value || ''} onChange={(e) => onChange(Number(e.target.value))} required>
      <option value="" disabled>
        Choose…
      </option>
      {items.map((i) => (
        <option key={i.id} value={i.id}>
          {i.name}
        </option>
      ))}
    </select>
  )
}

export function CampaignDetail({ id, navigate }: { id: number; navigate: (p: string) => void }) {
  const { data, error, loading, reload } = useAsync(() => api.get<Campaign>(`/api/v1/campaigns/${id}`), [id])
  const [actionError, setActionError] = useState('')
  const [expanded, setExpanded] = useState<number | null>(null)
  const [session, setSession] = useState<{ target: number; upstream: string; cookies: Record<string, string> } | null>(null)
  const [analysis, setAnalysis] = useState('')
  const [analyzing, setAnalyzing] = useState(false)
  const ai = useAIStatus()

  async function loadSession(targetId: number) {
    setActionError('')
    try {
      const res = await api.get<{ upstream: string; cookies: Record<string, string> }>(
        `/api/v1/campaigns/${id}/targets/${targetId}/session`,
      )
      setSession({ target: targetId, ...res })
    } catch (err) {
      setActionError(errorText(err))
    }
  }

  const rows = useMemo(() => data?.results ?? [], [data])

  async function act(path: string, confirmText?: string) {
    if (confirmText && !window.confirm(confirmText)) return
    setActionError('')
    try {
      await api.post(`/api/v1/campaigns/${id}/${path}`)
      reload()
    } catch (err) {
      setActionError(errorText(err))
    }
  }

  async function analyze() {
    setAnalyzing(true)
    setActionError('')
    try {
      const res = await api.post<{ analysis: string }>(`/api/v1/campaigns/${id}/analyze`)
      setAnalysis(res.analysis)
    } catch (err) {
      setActionError(errorText(err))
    } finally {
      setAnalyzing(false)
    }
  }

  async function remove() {
    if (!window.confirm('Delete this campaign and its results?')) return
    try {
      await api.del(`/api/v1/campaigns/${id}`)
      navigate('/campaigns')
    } catch (err) {
      setActionError(errorText(err))
    }
  }

  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} />
  if (!data) return null
  const s = data.stats
  const running = data.status === 'sending' || data.status === 'queued'

  return (
    <>
      <div className="toolbar">
        <div>
          <h1 style={{ margin: 0 }}>{data.name}</h1>
          <span className="muted small">
            <Badge status={data.status} /> created {timeAgo(data.created_at)}
          </span>
        </div>
        <div className="grow" />
        {running ? (
          <button className="danger" onClick={() => act('stop', 'Stop this campaign now?')}>
            Stop
          </button>
        ) : (
          <button className="primary" onClick={() => act('launch', 'Launch this campaign now?')}>
            Launch
          </button>
        )}
        <button onClick={() => act('resend', 'Requeue all failed recipients?')}>Resend failed</button>
        <a className="badge info" href={`/api/v1/campaigns/${id}/export`} style={{ padding: '7px 12px' }}>
          Export CSV
        </a>
        <button className="danger" onClick={remove}>
          Delete
        </button>
      </div>
      <ErrorBox message={actionError} />

      {ai.enabled && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="toolbar">
            <h2 style={{ margin: 0 }}>AI analysis</h2>
            <div className="grow" />
            <button onClick={analyze} disabled={analyzing}>
              {analyzing ? 'Analysing…' : analysis ? 'Re-run analysis' : 'Analyse this campaign'}
            </button>
          </div>
          {analysis ? (
            <div className="prewrap">{analysis}</div>
          ) : (
            <p className="muted small" style={{ margin: 0 }}>
              Sends aggregate numbers only — no recipient addresses or captured values — to the configured AI
              endpoint.
            </p>
          )}
        </div>
      )}

      <div className="grid cols-4" style={{ marginBottom: 14 }}>
        <Metric label="Recipients" value={s.total} sub={`${s.pending} pending`} />
        <Metric label="Sent" value={s.sent} sub={`${s.failed} failed`} />
        <Metric label="Opened" value={pct(s.opened, s.sent)} sub={`${s.opened} recipients`} />
        <Metric label="Clicked" value={pct(s.clicked, s.sent)} sub={`${s.clicked} recipients`} />
        <Metric label="Submitted" value={pct(s.submitted, s.sent)} sub={`${s.submitted} recipients`} />
        <Metric label="Reported" value={pct(s.reported, s.sent)} sub={`${s.reported} recipients`} />
        <Metric label="Uncertain" value={s.uncertain} sub="may have been delivered" />
        <Metric label="Opted out" value={s.opted_out} />
      </div>

      <div className="card">
        <h2>Recipients</h2>
        <table>
          <thead>
            <tr>
              <th>Email</th>
              <th>Name</th>
              <th>Status</th>
              <th>Opened</th>
              <th>Clicked</th>
              <th>Submitted</th>
              <th>Reported</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((t) => (
              <>
                <tr key={t.id}>
                  <td>{t.email}</td>
                  <td>
                    {t.first_name} {t.last_name}
                  </td>
                  <td>
                    <Badge status={t.status} />
                    {t.error && <div className="small" style={{ color: 'var(--bad)' }}>{t.error}</div>}
                  </td>
                  <td className="small muted">{timeAgo(t.opened_at)}</td>
                  <td className="small muted">{timeAgo(t.clicked_at)}</td>
                  <td className="small muted">{timeAgo(t.submitted_at)}</td>
                  <td className="small muted">{timeAgo(t.reported_at)}</td>
                  <td className="actions">
                    {(t.fields?.length || (t.data && Object.keys(t.data).length > 0)) && (
                      <button onClick={() => setExpanded(expanded === t.id ? null : t.id)}>
                        {expanded === t.id ? 'Hide' : 'Data'}
                      </button>
                    )}{' '}
                    {t.has_session && <button onClick={() => loadSession(t.id)}>Session</button>}
                  </td>
                </tr>
                {expanded === t.id && (
                  <tr key={`${t.id}-data`}>
                    <td colSpan={8}>
                      {t.data && Object.keys(t.data).length > 0 ? (
                        <div className="mono prewrap">{JSON.stringify(t.data, null, 2)}</div>
                      ) : (
                        <div className="muted small">
                          Fields submitted: {(t.fields ?? []).join(', ') || 'none'}
                          <br />
                          Values were not stored for this page.
                        </div>
                      )}
                    </td>
                  </tr>
                )}
              </>
            ))}
          </tbody>
        </table>
      </div>

      {session && (
        <div className="card">
          <div className="toolbar">
            <h2 style={{ margin: 0 }}>Captured session</h2>
            <span className="muted small">{session.upstream}</span>
            <div className="grow" />
            <button onClick={() => setSession(null)}>Close</button>
          </div>
          <p className="muted small">
            Replay these cookies against {session.upstream} to see what the relayed session had access to. Treat them
            like live credentials.
          </p>
          <div className="mono prewrap">{JSON.stringify(session.cookies, null, 2)}</div>
        </div>
      )}

      <div className="card">
        <h2>Timeline</h2>
        <ul className="timeline">
          {(data.timeline ?? []).map((e) => (
            <li key={e.id}>
              <span className="badge info">{e.type}</span>
              <span>
                {e.message}
                {e.email && <span className="muted"> · {e.email}</span>}
                {e.ip && <span className="muted small"> · {e.ip}</span>}
              </span>
              <span className="when">{timeAgo(e.at)}</span>
            </li>
          ))}
        </ul>
        {(data.timeline ?? []).length === 0 && <p className="muted">No events yet.</p>}
      </div>
    </>
  )
}
