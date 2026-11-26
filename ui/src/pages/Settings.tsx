import { useState } from 'react'
import { api, errorText, timeAgo } from '../api'
import { ErrorBox, Field, Loading, useAsync } from '../ui'
import type { Me } from '../App'

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

type AISettings = {
  enabled: boolean
  base_url: string
  model: string
  has_api_key: boolean
  timeout_seconds: number
}

type SettingsData = {
  org_name: string
  phish_url: string
  admin_url: string
  version: string
  events: string[]
  imap: ImapSettings
  ai: AISettings
}

type Tab = 'account' | 'application' | 'reporting' | 'ai' | 'team' | 'audit'

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
      {isAdmin && (tab === 'application' || tab === 'reporting' || tab === 'ai') && (
        settings.loading ? <Loading /> :
        settings.data && (
          <>
            {tab === 'application' && <Application data={settings.data} onSaved={load} />}
            {tab === 'reporting' && <Reporting data={settings.data} onSaved={load} />}
            {tab === 'ai' && <AI data={settings.data} onSaved={load} />}
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
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNote('')
    try {
      await api.put('/api/v1/settings', { org_name: orgName, phish_public_url: phishURL })
      setNote('Saved.')
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
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
          label="Tracking link base URL"
          hint="Origin used in links recipients click. Must be reachable from outside; TLS must match this hostname."
        >
          <input value={phishURL} onChange={(e) => setPhishURL(e.target.value)} placeholder="https://training.example.com" />
        </Field>
        <button className="primary" type="submit" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </button>
        {note && <span className="hint" style={{ marginLeft: 10 }}>{note}</span>}
      </form>

      <div style={{ marginTop: 20, borderTop: '1px solid var(--line)', paddingTop: 14 }}>
        <h3>Read-only</h3>
        <table>
          <tbody>
            <tr>
              <td className="muted">Admin URL</td>
              <td className="mono">{data.admin_url}</td>
            </tr>
            <tr>
              <td className="muted">Version</td>
              <td className="mono">{data.version}</td>
            </tr>
            <tr>
              <td className="muted">Listeners, database path, encryption key, relay allowlist</td>
              <td className="muted small">Set in config.json; they need a restart</td>
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
