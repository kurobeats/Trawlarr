import { useState } from 'react'
import { api, errorText, timeAgo } from '../api'
import { ErrorBox, Field, Loading, useAsync } from '../ui'
import type { Me } from '../App'

type User = {
  id: number
  username: string
  email: string
  role: string
  disabled: boolean
  totp_enabled: boolean
  created_at: string
}

type AuditEntry = {
  id: number
  username: string
  action: string
  target: string
  detail: string
  ip: string
  at: string
}

export function Settings({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const [tab, setTab] = useState<'account' | 'team' | 'audit'>('account')
  return (
    <>
      <h1>Settings</h1>
      <div className="tabs">
        <button className={tab === 'account' ? 'active' : ''} onClick={() => setTab('account')}>
          Account
        </button>
        {me.role === 'admin' && (
          <button className={tab === 'team' ? 'active' : ''} onClick={() => setTab('team')}>
            Team
          </button>
        )}
        <button className={tab === 'audit' ? 'active' : ''} onClick={() => setTab('audit')}>
          Audit log
        </button>
      </div>
      {tab === 'account' && <Account me={me} onMe={onMe} />}
      {tab === 'team' && me.role === 'admin' && <Team me={me} />}
      {tab === 'audit' && <Audit />}
    </>
  )
}

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
              Add this secret to your authenticator, then enter a code to confirm. Secret: <span className="mono">{totp.secret}</span>
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
          Send it as <span className="mono">Authorization: Bearer &lt;key&gt;</span>. Only a hash is stored, so the value is
          shown once.
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
                    <select
                      value={u.role}
                      onChange={(e) => patch(u.id, { role: e.target.value })}
                      style={{ width: 120 }}
                    >
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
