import { useState } from 'react'
import { api, errorText } from '../api'
import { ErrorBox, Field } from '../ui'
import type { Me } from '../App'

export function Login({ onLogin }: { onLogin: (me: Me) => void }) {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  const [needsTotp, setNeedsTotp] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const me = await api.post<Me>('/api/v1/login', { username, password, totp })
      onLogin(me)
    } catch (err: any) {
      // The API answers 401 with totp_required when a code is still needed.
      if (String(err.message).includes('totp')) setNeedsTotp(true)
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="login card" onSubmit={submit}>
        <div className="brand">
          <span className="dot" /> Trawlarr
        </div>
        <p className="muted small" style={{ textAlign: 'center', marginTop: 0 }}>
          Phishing simulation platform
        </p>
        <ErrorBox message={error} />
        <Field label="Username">
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus autoComplete="username" />
        </Field>
        <Field label="Password">
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </Field>
        {needsTotp && (
          <Field label="Authenticator code" hint="Six digits from your authenticator app.">
            <input value={totp} onChange={(e) => setTotp(e.target.value)} inputMode="numeric" maxLength={6} />
          </Field>
        )}
        <button className="primary" type="submit" disabled={busy} style={{ width: '100%' }}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
