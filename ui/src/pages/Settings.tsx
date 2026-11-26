import { useEffect, useState } from 'react'
import { api, errorText, timeAgo } from '../api'
import { ErrorBox, Field, Loading, useAsync } from '../ui'
import type { Me } from '../App'
import { ListenField, PathField } from '../pathfield'

type ImapSettings = {
  enabled: boolean
  host: string
  port: number
  username: string
  has_password: boolean
  use_tls: boolean
  ignore_cert: boolean
  folder: string
  restrict_domain: string
  delete_reported: boolean
  interval_minutes: number
}

type MCPSettings = {
  enabled: boolean
  allow_writes: boolean
  user: string
}

type AISettings = {
  enabled: boolean
  base_url: string
  model: string
  has_api_key: boolean
  timeout_seconds: number
}

type ApplicationSettings = {
  admin_listen: string
  phish_listen: string
  db_path: string
  proxy_enabled: boolean
  proxy_allowlist: string[]
}

type SettingsData = {
  org_name: string
  phish_url: string
  admin_url: string
  application: ApplicationSettings
  version_hint?: string
  version: string
  events: string[]
  imap: ImapSettings
  ai: AISettings
  mcp: MCPSettings
}

type Tab = 'account' | 'application' | 'reporting' | 'ai' | 'geoip' | 'mcp' | 'team' | 'audit'

export function Settings({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const [tab, setTab] = useState<Tab>('account')
  const settings = useAsync(() => api.get<SettingsData>('/api/v1/settings'))
  const isAdmin = me.role === 'admin'

  const load = () => settings.reload()

  return (
    <>
      <h1>Settings</h1>
      <div className="tabs">
        <button className={tab === 'account' ? 'active' : ''} onClick={() => setTab('account')}>
          Account
        </button>
        {isAdmin && (
          <button className={tab === 'application' ? 'active' : ''} onClick={() => setTab('application')}>
            Application
          </button>
        )}
        {isAdmin && (
          <button className={tab === 'reporting' ? 'active' : ''} onClick={() => setTab('reporting')}>
            Reporting
          </button>
        )}
        {isAdmin && (
          <button className={tab === 'ai' ? 'active' : ''} onClick={() => setTab('ai')}>
            AI
          </button>
        )}
        {isAdmin && (
          <button className={tab === 'geoip' ? 'active' : ''} onClick={() => setTab('geoip')}>
            GeoIP
          </button>
        )}
        {isAdmin && (
          <button className={tab === 'mcp' ? 'active' : ''} onClick={() => setTab('mcp')}>
            MCP
          </button>
        )}
        {isAdmin && (
          <button className={tab === 'team' ? 'active' : ''} onClick={() => setTab('team')}>
            Team
          </button>
        )}
        <button className={tab === 'audit' ? 'active' : ''} onClick={() => setTab('audit')}>
          Audit log
        </button>
      </div>

      <ErrorBox message={settings.error} />

      {tab === 'account' && <Account me={me} onMe={onMe} />}
      {isAdmin && (tab === 'application' || tab === 'reporting' || tab === 'ai' || tab === 'mcp' || tab === 'geoip') && (
        settings.loading ? <Loading /> :
        settings.data && (
          <>
            {tab === 'application' && <Application data={settings.data} onSaved={load} />}
            {tab === 'reporting' && <Reporting data={settings.data} onSaved={load} />}
            {tab === 'ai' && <AI data={settings.data} onSaved={load} />}
            {tab === 'geoip' && <GeoIP />}
            {tab === 'mcp' && <MCP data={settings.data} onSaved={load} />}
          </>
        )
      )}
      {isAdmin && tab === 'team' && <Team me={me} />}
      {tab === 'audit' && <Audit />}
    </>
  )
}

// ------------------------------------------------------------------ application

function Application({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const [orgName, setOrgName] = useState(data.org_name)
  const [phishURL, setPhishURL] = useState(data.phish_url)
  const [adminURL, setAdminURL] = useState(data.admin_url)
  const [adminListen, setAdminListen] = useState(data.application.admin_listen)
  const [phishListen, setPhishListen] = useState(data.application.phish_listen)
  const [dbPath, setDBPath] = useState(data.application.db_path)
  const [relayOn, setRelayOn] = useState(data.application.proxy_enabled)
  const [allowlist, setAllowlist] = useState(data.application.proxy_allowlist.join(', '))
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [restart, setRestart] = useState<string[]>([])
  const [restarting, setRestarting] = useState(false)

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNote('')
    setRestart([])
    try {
      const res = await api.put<{ restart_required?: boolean; restart_reasons?: string[] }>('/api/v1/settings', {
        org_name: orgName,
        phish_public_url: phishURL,
        admin_public_url: adminURL,
        application: {
          admin_listen: adminListen,
          phish_listen: phishListen,
          db_path: dbPath,
          proxy_enabled: relayOn,
          proxy_allowlist: allowlist.split(/[\s,]+/).filter(Boolean),
        },
      })
      setNote('Saved.')
      if (res?.restart_required) setRestart(res.restart_reasons ?? [])
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  async function restartNow() {
    if (!window.confirm('Restart the server now? The UI reconnects automatically.')) return
    setRestarting(true)
    try {
      await api.post('/api/v1/power/restart')
    } catch {
      // the connection usually drops before the response is read
    }
    const timer = window.setInterval(async () => {
      try {
        await api.get('/api/v1/health')
        window.clearInterval(timer)
        window.location.href = adminURL || window.location.origin
      } catch {
        // still starting
      }
    }, 1500)
  }

  return (
    <div className="card">
      <h2>Application</h2>
      <ErrorBox message={error} />
      <form onSubmit={save} style={{ maxWidth: 520 }}>
        <Field label="Organisation name">
          <input value={orgName} onChange={(e) => setOrgName(e.target.value)} required />
        </Field>
        <Field
          label="Admin URL"
          hint="Where you reach this console. Used for cross-origin checks and shown in the sidebar."
        >
          <input value={adminURL} onChange={(e) => setAdminURL(e.target.value)} placeholder="https://trawlarr.example.com" />
        </Field>
        <Field
          label="Tracking link base URL"
          hint="Origin used in links recipients click. Must be reachable from outside; TLS must match this hostname."
        >
          <input value={phishURL} onChange={(e) => setPhishURL(e.target.value)} placeholder="https://training.example.com" />
        </Field>
        <h3 style={{ marginTop: 20 }}>Listeners</h3>
        <div className="row">
          <div style={{ flex: 1 }}>
            <ListenField
              label="Admin listener"
              hint="Where this console listens, e.g. 127.0.0.1:3333."
              value={adminListen}
              onChange={setAdminListen}
              placeholder="127.0.0.1:3333"
            />
          </div>
          <div style={{ flex: 1 }}>
            <ListenField
              label="Phishing listener"
              hint="Tracking and landing pages, e.g. 0.0.0.0:8080."
              value={phishListen}
              onChange={setPhishListen}
              placeholder="0.0.0.0:8080"
            />
          </div>
        </div>

        <PathField
          label="Database path"
          hint="SQLite file. Type to browse: matching directories are offered as you go. Pointing at a new path starts from an empty database — existing data is not copied."
          value={dbPath}
          onChange={setDBPath}
          placeholder="trawlarr.db"
        />

        <h3 style={{ marginTop: 20 }}>Relay campaigns</h3>
        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={relayOn}
            onChange={(e) => setRelayOn(e.target.checked)}
          />
          Allow relaying a live site (AiTM)
        </label>
        <p className="hint" style={{ marginTop: 6 }}>
          Only hosts listed below can be relayed. This applies immediately; no restart needed.
        </p>
        <Field label="Allowed hostnames" hint="Comma or space separated; a leading *. allows subdomains. Empty means nothing is allowed.">
          <input value={allowlist} onChange={(e) => setAllowlist(e.target.value)} placeholder="login.example.com, *.corp.internal" />
        </Field>

        <button className="primary" type="submit" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </button>
        {note && <span className="hint" style={{ marginLeft: 10 }}>{note}</span>}
      </form>

      {restart.length > 0 && (
        <div className="card" style={{ background: 'var(--panel-2)', marginTop: 14 }}>
          <strong>Restart needed</strong>
          <p className="muted" style={{ margin: '6px 0 10px' }}>
            {restart.join(' and ')} changed. Restart from the <strong>Power</strong> menu, or with the button below, to
            apply it. Sessions are kept and campaigns that were sending resume.
          </p>
          <div className="row" style={{ alignItems: 'center' }}>
            <button className="primary" onClick={restartNow} disabled={restarting}>
              {restarting ? 'Restarting…' : 'Restart now'}
            </button>
            <span className="hint">The Power menu in the sidebar does the same thing.</span>
          </div>
        </div>
      )}

      <div style={{ marginTop: 20, borderTop: '1px solid var(--line)', paddingTop: 14 }}>
        <h3>Read-only</h3>
        <table>
          <tbody>
            <tr>
              <td className="muted">Version</td>
              <td className="mono">{data.version}</td>
            </tr>
            <tr>
              <td className="muted">Encryption key, TLS certificate files</td>
              <td className="muted small">Set in config.json</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  )
}

// ------------------------------------------------------------------- reporting

function Reporting({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const [form, setForm] = useState(data.imap)
  const [password, setPassword] = useState('')
  const [clear, setClear] = useState(false)
  const [advanced, setAdvanced] = useState(
    Boolean(data.imap.folder !== 'INBOX' || data.imap.restrict_domain || data.imap.delete_reported),
  )
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)

  const set = (patch: Partial<ImapSettings>) => setForm({ ...form, ...patch })

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNote('')
    try {
      await api.put('/api/v1/settings', {
        imap: { ...form, password, clear_password: clear },
      })
      setPassword('')
      setClear(false)
      setNote('Saved. The monitor picks this up within one poll.')
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <h2>Reporting</h2>
      <p className="muted" style={{ marginTop: 0 }}>
        Watch a mailbox for messages recipients report. Reports are matched by scanning the message and any forwarded
        attachment for the tracking parameter.
      </p>
      <ErrorBox message={error} />
      <form onSubmit={save} style={{ maxWidth: 560 }}>
        <label className="chip">
          <input type="checkbox" style={{ width: 'auto' }} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          Enable mailbox monitoring
        </label>
        <div style={{ height: 12 }} />
        <div className="row">
          <div style={{ flex: 2 }}>
            <Field label="IMAP host">
              <input value={form.host} onChange={(e) => set({ host: e.target.value })} placeholder="imap.example.com" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Port">
              <input type="number" value={form.port} onChange={(e) => set({ port: Number(e.target.value) })} />
            </Field>
          </div>
        </div>
        <div className="row">
          <div style={{ flex: 1 }}>
            <Field label="Username">
              <input value={form.username} onChange={(e) => set({ username: e.target.value })} autoComplete="off" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Password" hint={form.has_password ? 'Stored. Leave blank to keep it.' : 'Stored encrypted.'}>
              <input
                type="password"
                value={password}
                onChange={(e) => { setPassword(e.target.value); setClear(false) }}
                autoComplete="new-password"
                placeholder={form.has_password ? '••••••••' : ''}
              />
            </Field>
          </div>
        </div>
        <div className="row">
          <label className="chip">
            <input type="checkbox" style={{ width: 'auto' }} checked={form.use_tls} onChange={(e) => set({ use_tls: e.target.checked })} />
            Use TLS
          </label>
          {form.has_password && (
            <label className="chip">
              <input type="checkbox" style={{ width: 'auto' }} checked={clear} onChange={(e) => setClear(e.target.checked)} />
              Remove stored password
            </label>
          )}
          <button type="button" onClick={() => setAdvanced(!advanced)}>
            {advanced ? 'Hide advanced' : 'Advanced'}
          </button>
        </div>

        {advanced && (
          <>
            <div style={{ height: 12 }} />
            <div className="row">
              <div style={{ flex: 1 }}>
                <Field label="Folder" hint="Defaults to INBOX.">
                  <input value={form.folder} onChange={(e) => set({ folder: e.target.value })} />
                </Field>
              </div>
              <div style={{ flex: 1 }}>
                <Field label="Poll every (minutes)">
                  <input
                    type="number"
                    min={1}
                    max={1440}
                    value={form.interval_minutes}
                    onChange={(e) => set({ interval_minutes: Number(e.target.value) })}
                  />
                </Field>
              </div>
            </div>
            <div className="row">
              <div style={{ flex: 1 }}>
                <Field label="Only accept reports from this domain" hint="Leave blank to accept any sender.">
                  <input value={form.restrict_domain} onChange={(e) => set({ restrict_domain: e.target.value })} placeholder="corp.example" />
                </Field>
              </div>
              <div style={{ flex: 1 }}>
                <Field label="Certificates">
                  <label className="chip">
                    <input
                      type="checkbox"
                      style={{ width: 'auto' }}
                      checked={form.ignore_cert}
                      onChange={(e) => set({ ignore_cert: e.target.checked })}
                    />
                    Ignore certificate errors
                  </label>
                </Field>
              </div>
            </div>
            <label className="chip">
              <input
                type="checkbox"
                style={{ width: 'auto' }}
                checked={form.delete_reported}
                onChange={(e) => set({ delete_reported: e.target.checked })}
              />
              Delete reported messages after processing
            </label>
          </>
        )}

        <div style={{ marginTop: 14 }}>
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          {note && <span className="hint" style={{ marginLeft: 10 }}>{note}</span>}
        </div>
      </form>
    </div>
  )
}

// -------------------------------------------------------------------------- AI

function AI({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const [form, setForm] = useState(data.ai)
  const [apiKey, setAPIKey] = useState('')
  const [clear, setClear] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const set = (patch: Partial<AISettings>) => setForm({ ...form, ...patch })

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNote('')
    try {
      await api.put('/api/v1/settings', { ai: { ...form, api_key: apiKey, clear_api_key: clear } })
      setAPIKey('')
      setClear(false)
      setNote('Saved. Drafting and analysis are available immediately.')
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <h2>AI</h2>
      <p className="muted" style={{ marginTop: 0 }}>
        Any OpenAI-compatible endpoint. Used for drafting content and for campaign analysis; analysis only ever sends
        aggregate numbers.
      </p>
      <ErrorBox message={error} />
      <form onSubmit={save} style={{ maxWidth: 560 }}>
        <label className="chip">
          <input type="checkbox" style={{ width: 'auto' }} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          Enable AI drafting and analysis
        </label>
        <div style={{ height: 12 }} />
        <Field label="Base URL" hint="Include the version prefix, for example http://127.0.0.1:11434/v1">
          <input
            value={form.base_url}
            onChange={(e) => set({ base_url: e.target.value })}
            placeholder="https://api.openai.com/v1"
          />
        </Field>
        <div className="row">
          <div style={{ flex: 2 }}>
            <Field label="Model">
              <input value={form.model} onChange={(e) => set({ model: e.target.value })} placeholder="gpt-4o-mini" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Timeout (seconds)">
              <input
                type="number"
                min={5}
                max={600}
                value={form.timeout_seconds}
                onChange={(e) => set({ timeout_seconds: Number(e.target.value) })}
              />
            </Field>
          </div>
        </div>
        <Field
          label="API key"
          hint={
            form.has_api_key
              ? 'Stored. Leave blank to keep it. TRAWLARR_AI_API_KEY in the environment also works.'
              : 'Stored encrypted. TRAWLARR_AI_API_KEY in the environment also works.'
          }
        >
          <input
            type="password"
            value={apiKey}
            onChange={(e) => { setAPIKey(e.target.value); setClear(false) }}
            autoComplete="new-password"
            placeholder={form.has_api_key ? '••••••••' : ''}
          />
        </Field>
        {form.has_api_key && (
          <label className="chip">
            <input type="checkbox" style={{ width: 'auto' }} checked={clear} onChange={(e) => setClear(e.target.checked)} />
            Remove stored key
          </label>
        )}
        <div style={{ marginTop: 14 }}>
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          {note && <span className="hint" style={{ marginLeft: 10 }}>{note}</span>}
        </div>
      </form>
    </div>
  )
}

// ----------------------------------------------------------------------- GeoIP

type GeoDBInfo = {
  installed: boolean
  path: string
  size: number
  modified: string | null
  database_type?: string
  description?: string
  error?: string
}

type GeoStatus = {
  available: boolean
  database: GeoDBInfo | null
  download?: { running: boolean; message: string; url: string; error: string }
  hint?: string
}

function GeoIP() {
  const [status, setStatus] = useState<GeoStatus | null>(null)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [testIP, setTestIP] = useState('')
  const [testResult, setTestResult] = useState<string>('')
  const [downloading, setDownloading] = useState(false)

  async function refresh() {
    try {
      setStatus(await api.get<GeoStatus>('/api/v1/geoip'))
    } catch (err) {
      setError(errorText(err))
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Poll while a download runs in the background.
  useEffect(() => {
    if (!downloading) return
    const timer = window.setInterval(async () => {
      try {
        const s = await api.get<GeoStatus>('/api/v1/geoip')
        setStatus(s)
        if (!s.download?.running) {
          setDownloading(false)
          if (s.download?.error) setError(s.download.error)
          else setNote(s.download?.message || 'Installed.')
        }
      } catch {
        // keep polling
      }
    }, 3000)
    return () => window.clearInterval(timer)
  }, [downloading])

  async function upload(file: File) {
    setBusy(true)
    setError('')
    setNote('')
    try {
      const form = new FormData()
      form.append('file', file)
      const res = await fetch('/api/v1/geoip/upload', { method: 'POST', body: form, credentials: 'same-origin' })
      const body = await res.json()
      if (!res.ok) throw new Error(body.error ?? 'upload failed')
      setStatus(body)
      setNote('Database installed.')
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  async function download() {
    setError('')
    setNote('')
    try {
      await api.post('/api/v1/geoip/download')
      setDownloading(true)
      setNote('Downloading the DB-IP Lite database…')
    } catch (err) {
      setError(errorText(err))
    }
  }

  async function lookup(e: React.FormEvent) {
    e.preventDefault()
    setTestResult('')
    setError('')
    try {
      const res = await api.post<{ found: boolean; ip: string; location?: { city: string; country: string; latitude: number; longitude: number } }>(
        '/api/v1/geoip/lookup',
        { ip: testIP },
      )
      setTestResult(
        res.found && res.location
          ? `${res.ip} → ${res.location.city || '?'}, ${res.location.country || '?'} (${res.location.latitude.toFixed(2)}, ${res.location.longitude.toFixed(2)})`
          : `${res.ip} → not in the database`,
      )
    } catch (err) {
      setError(errorText(err))
    }
  }

  const db = status?.database
  return (
    <div className="card">
      <h2>GeoIP</h2>
      <p className="muted" style={{ marginTop: 0 }}>
        Annotates the first open or click of each recipient with a location and draws it on the campaign map. Lookups
        happen on this server — no address is sent to a third party.
      </p>
      <ErrorBox message={error} />
      {note && <p className="hint okmsg">{note}</p>}

      <h3>Installed database</h3>
      {!db?.installed ? (
        <p className="muted">No database installed. Nothing is geolocated until one is.</p>
      ) : (
        <table style={{ marginBottom: 12 }}>
          <tbody>
            <tr>
              <td className="muted">Type</td>
              <td className="mono">{db.database_type || 'unknown'}</td>
            </tr>
            <tr>
              <td className="muted">Description</td>
              <td>{db.description || '—'}</td>
            </tr>
            <tr>
              <td className="muted">Size</td>
              <td className="mono">{(db.size / (1024 * 1024)).toFixed(1)} MB</td>
            </tr>
            <tr>
              <td className="muted">Updated</td>
              <td className="mono">{db.modified ? timeAgo(db.modified) : '—'}</td>
            </tr>
            <tr>
              <td className="muted">Path</td>
              <td className="mono small">{db.path}</td>
            </tr>
          </tbody>
        </table>
      )}
      {db?.error && <div className="error">{db.error}</div>}

      <div className="row" style={{ alignItems: 'center' }}>
        <button onClick={download} disabled={downloading}>
          {downloading ? 'Downloading…' : 'Download DB-IP Lite'}
        </button>
        <input
          type="file"
          accept=".mmdb,.gz"
          disabled={busy}
          onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])}
        />
      </div>

      <p className="hint">
        <strong>Download</strong> fetches the free DB-IP Lite city database (~100 MB, no account) as its monthly
        release from <span className="mono">db-ip.com</span>; it is licensed CC BY 4.0 and needs attribution if you
        publish maps. <strong>Upload</strong> accepts any MaxMind-format city database instead — for example MaxMind's
        own GeoLite2-City (free account) or a commercial GeoIP2 file.
      </p>

      <h3 style={{ marginTop: 18 }}>Check an address</h3>
      <form onSubmit={lookup} className="row" style={{ alignItems: 'flex-end', maxWidth: 420 }}>
        <div style={{ flex: 1 }}>
          <Field label="IP address">
            <input value={testIP} onChange={(e) => setTestIP(e.target.value)} placeholder="203.0.113.42" />
          </Field>
        </div>
        <button type="submit" style={{ marginBottom: 12 }}>
          Look up
        </button>
      </form>
      {testResult && <div className="mono small">{testResult}</div>}
    </div>
  )
}

// ------------------------------------------------------------------------- MCP

function MCP({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const [form, setForm] = useState(data.mcp)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const users = useAsync(() => api.get<{ id: number; username: string; disabled: boolean }[]>('/api/v1/users'))
  const set = (patch: Partial<MCPSettings>) => setForm({ ...form, ...patch })

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNote('')
    try {
      await api.put('/api/v1/settings', { mcp: form })
      setNote('Saved. Write permission and the acting account apply to the next tool call.')
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <h2>MCP</h2>
      <p className="muted" style={{ marginTop: 0 }}>
        The Model Context Protocol server is a separate process your assistant launches (<span className="mono">trawlarr
        mcp</span>). It speaks stdio only — no port, no network listener.
      </p>
      <ErrorBox message={error} />
      <form onSubmit={save} style={{ maxWidth: 560 }}>
        <label className="chip">
          <input type="checkbox" style={{ width: 'auto' }} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          Allow MCP clients to connect
        </label>
        <p className="hint">
          Turning this off stops tool calls on the next request. Turning it on needs the MCP client to be restarted so
          it re-launches the process.
        </p>

        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={form.allow_writes}
            onChange={(e) => set({ allow_writes: e.target.checked })}
          />
          Allow tools that change data
        </label>
        <p className="hint">
          Creates, launches and stops. Without it the server is read-only. Applies immediately.
        </p>

        <Field label="Act as" hint="Tool calls and audit entries are attributed to this account. Defaults to the first admin.">
          <select value={form.user} onChange={(e) => set({ user: e.target.value })}>
            <option value="">First admin (default)</option>
            {(users.data ?? [])
              .filter((u) => !u.disabled)
              .map((u) => (
                <option key={u.id} value={u.username}>
                  {u.username}
                </option>
              ))}
          </select>
        </Field>

        <button className="primary" type="submit" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </button>
        {note && <span className="hint" style={{ marginLeft: 10 }}>{note}</span>}
      </form>

    </div>
  )
}

// ---------------------------------------------------------------------- account

function Account({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [note, setNote] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [totp, setTotp] = useState<{ secret: string; url: string } | null>(null)
  const [code, setCode] = useState('')

  async function changePassword(e: React.FormEvent) {
    e.preventDefault()
    setNote('')
    try {
      await api.post('/api/v1/me/password', { current, new: next })
      setCurrent('')
      setNext('')
      setNote('Password changed. Other sessions were signed out.')
    } catch (err) {
      setNote(errorText(err))
    }
  }

  async function rotateKey() {
    if (!window.confirm('Generate a new API key? The old key stops working immediately.')) return
    try {
      const res = await api.post<{ api_key: string }>('/api/v1/me/apikey')
      setApiKey(res.api_key)
      onMe({ ...me, has_api_key: true })
    } catch (err) {
      setNote(errorText(err))
    }
  }

  async function startTOTP() {
    setNote('')
    try {
      setTotp(await api.post<{ secret: string; url: string }>('/api/v1/me/totp/setup'))
    } catch (err) {
      setNote(errorText(err))
    }
  }

  async function enableTOTP() {
    setNote('')
    try {
      await api.post('/api/v1/me/totp/enable', { code })
      onMe({ ...me, totp_enabled: true })
      setTotp(null)
      setCode('')
      setNote('Two-factor authentication enabled.')
    } catch (err) {
      setNote(errorText(err))
    }
  }

  async function disableTOTP() {
    const password = window.prompt('Confirm your password to disable two-factor authentication')
    if (!password) return
    setNote('')
    try {
      await api.post('/api/v1/me/totp/disable', { password })
      onMe({ ...me, totp_enabled: false })
      setNote('Two-factor authentication disabled.')
    } catch (err) {
      setNote(errorText(err))
    }
  }

  return (
    <>
      <div className="card">
        <h2>Change password</h2>
        <form onSubmit={changePassword} style={{ maxWidth: 420 }}>
          <Field label="Current password">
            <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />
          </Field>
          <Field label="New password" hint="At least 12 characters.">
            <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
          </Field>
          <button className="primary" type="submit">
            Update password
          </button>
        </form>
        {note && <p className="hint">{note}</p>}
      </div>

      <div className="card">
        <h2>Two-factor authentication</h2>
        {me.totp_enabled ? (
          <>
            <p className="muted">Enabled.</p>
            <button className="danger" onClick={disableTOTP}>
              Disable
            </button>
          </>
        ) : totp ? (
          <>
            <p className="muted">
              Add this secret to your authenticator, then enter a code to confirm. Secret:{' '}
              <span className="mono">{totp.secret}</span>
            </p>
            <div className="row" style={{ maxWidth: 360 }}>
              <input style={{ flex: 1 }} value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" maxLength={6} />
              <button className="primary" onClick={enableTOTP}>
                Enable
              </button>
            </div>
          </>
        ) : (
          <button onClick={startTOTP}>Set up authenticator</button>
        )}
      </div>

      <div className="card">
        <h2>API key</h2>
        <p className="muted">
          Send it as <span className="mono">Authorization: Bearer &lt;key&gt;</span>. Only a hash is stored, so the value
          is shown once.
        </p>
        {apiKey && (
          <p className="mono prewrap" style={{ background: 'var(--panel-2)', padding: 10, borderRadius: 8 }}>
            {apiKey}
          </p>
        )}
        <button onClick={rotateKey}>{me.has_api_key ? 'Rotate API key' : 'Create API key'}</button>
      </div>
    </>
  )
}

// ------------------------------------------------------------------------- team

function Team({ me }: { me: Me }) {
  const users = useAsync(() => api.get<User[]>('/api/v1/users'))
  const [error, setError] = useState('')
  const [form, setForm] = useState({ username: '', email: '', password: '', role: 'operator' })
  const [creating, setCreating] = useState(false)

  async function create(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    try {
      await api.post('/api/v1/users', form)
      setCreating(false)
      setForm({ username: '', email: '', password: '', role: 'operator' })
      users.reload()
    } catch (err) {
      setError(errorText(err))
    }
  }

  async function patch(id: number, body: unknown) {
    setError('')
    try {
      await api.put(`/api/v1/users/${id}`, body)
      users.reload()
    } catch (err) {
      setError(errorText(err))
    }
  }

  async function remove(u: User) {
    if (!window.confirm(`Delete ${u.username}?`)) return
    try {
      await api.del(`/api/v1/users/${u.id}`)
      users.reload()
    } catch (err) {
      setError(errorText(err))
    }
  }

  return (
    <>
      <ErrorBox message={error} />
      <div className="card">
        <div className="toolbar">
          <h2 style={{ margin: 0 }}>Users</h2>
          <div className="grow" />
          <button className="primary" onClick={() => setCreating(!creating)}>
            Add user
          </button>
        </div>
        {creating && (
          <form onSubmit={create} className="row" style={{ marginBottom: 14, alignItems: 'flex-end' }}>
            <div style={{ flex: 1 }}>
              <Field label="Username">
                <input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} required />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Email">
                <input value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label="Password">
                <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required />
              </Field>
            </div>
            <div style={{ width: 130 }}>
              <Field label="Role">
                <select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
                  <option value="admin">admin</option>
                  <option value="operator">operator</option>
                  <option value="viewer">viewer</option>
                </select>
              </Field>
            </div>
            <button className="primary" type="submit" style={{ marginBottom: 12 }}>
              Create
            </button>
          </form>
        )}
        {users.loading ? (
          <Loading />
        ) : (
          <table>
            <thead>
              <tr>
                <th>Username</th>
                <th>Email</th>
                <th>Role</th>
                <th>2FA</th>
                <th>State</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {users.data?.map((u) => (
                <tr key={u.id}>
                  <td>{u.username}</td>
                  <td className="muted">{u.email || '—'}</td>
                  <td>
                    <select value={u.role} onChange={(e) => patch(u.id, { role: e.target.value })} style={{ width: 120 }}>
                      <option value="admin">admin</option>
                      <option value="operator">operator</option>
                      <option value="viewer">viewer</option>
                    </select>
                  </td>
                  <td className="muted">{u.totp_enabled ? 'on' : 'off'}</td>
                  <td>{u.disabled ? <span className="badge bad">disabled</span> : <span className="badge ok">active</span>}</td>
                  <td className="actions">
                    {u.id !== me.id && (
                      <>
                        <button onClick={() => patch(u.id, { disabled: !u.disabled })}>{u.disabled ? 'Enable' : 'Disable'}</button>{' '}
                        <button className="danger" onClick={() => remove(u)}>
                          Delete
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}

type User = {
  id: number
  username: string
  email: string
  role: string
  disabled: boolean
  totp_enabled: boolean
  created_at: string
}

// ------------------------------------------------------------------------ audit

type AuditEntry = {
  id: number
  username: string
  action: string
  target: string
  detail: string
  ip: string
  at: string
}

function Audit() {
  const entries = useAsync(() => api.get<AuditEntry[]>('/api/v1/audit?limit=200'))
  if (entries.loading) return <Loading />
  return (
    <div className="card">
      <ErrorBox message={entries.error} />
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>User</th>
            <th>Action</th>
            <th>Target</th>
            <th>Detail</th>
            <th>IP</th>
          </tr>
        </thead>
        <tbody>
          {entries.data?.map((e) => (
            <tr key={e.id}>
              <td className="muted small">{timeAgo(e.at)}</td>
              <td>{e.username || '—'}</td>
              <td className="mono small">{e.action}</td>
              <td>{e.target}</td>
              <td className="muted small">{e.detail}</td>
              <td className="muted small">{e.ip}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
