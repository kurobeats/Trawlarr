import { useState } from 'react'
import { api, errorText, timeAgo } from '../api'
import { ErrorBox, Field, Loading, Modal, useAsync } from '../ui'

type CachedCert = {
  domain: string
  issuer: string
  not_after: string | null
  dns_names: string[]
}

type Certificate = {
  id: number
  name: string
  domains: string[]
  email: string
  staging: boolean
  accept_tos: boolean
  cache_dir: string
  http_addr: string
  usage: number
  status: {
    certificates: CachedCert[]
    renews_at: string | null
    cache_dir: string
    http_addr: string
    error?: string
  }
}

export function Certificates() {
  const list = useAsync(() => api.get<Certificate[]>('/api/v1/certificates'))
  const [editing, setEditing] = useState<Certificate | null>(null)
  const [busy, setBusy] = useState<number | null>(null)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')

  async function issue(cert: Certificate, action: 'request' | 'renew') {
    const question =
      action === 'renew'
        ? `Renew the certificate for ${cert.domains.join(', ')} now?`
        : `Request a certificate for ${cert.domains.join(', ')}? This contacts Let's Encrypt and can take a minute.`
    if (!window.confirm(question)) return
    setBusy(cert.id)
    setError('')
    setNote('')
    try {
      const res = await api.post<{ status: { certificates: CachedCert[] } }>(
        `/api/v1/certificates/${cert.id}/${action}`,
      )
      const cert0 = res.status.certificates?.[0]
      setNote(cert0 ? `${action === 'renew' ? 'Renewed' : 'Issued'}: expires ${new Date(cert0.not_after ?? '').toLocaleString()}` : 'Done.')
      list.reload()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(null)
    }
  }

  async function remove(cert: Certificate) {
    if (!window.confirm(`Delete the certificate profile "${cert.name}"?`)) return
    setError('')
    try {
      await api.del(`/api/v1/certificates/${cert.id}`)
      list.reload()
    } catch (err) {
      setError(errorText(err))
    }
  }

  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Certificates</h1>
        <div className="grow" />
        <button className="primary" onClick={() => setEditing(blankCertificate())}>
          New certificate
        </button>
      </div>
      <p className="muted" style={{ marginTop: 0 }}>
        One profile per set of hostnames. Certificates are requested and renewed from here — never automatically — and
        the phishing listener picks the right one by SNI. Campaigns reference the profile that covers their tracking
        hostname.
      </p>

      <ErrorBox message={list.error || error} />
      {note && <p className="hint okmsg">{note}</p>}
      {list.loading ? (
        <Loading />
      ) : (
        <div className="card">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Domains</th>
                <th>Certificate</th>
                <th>Campaigns</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((cert) => {
                const cached = cert.status.certificates?.[0]
                const expires = cached?.not_after ? new Date(cached.not_after) : null
                const daysLeft = expires ? Math.round((expires.getTime() - Date.now()) / 86400000) : null
                const expired = daysLeft !== null && daysLeft < 0
                return (
                  <tr key={cert.id}>
                    <td>{cert.name}</td>
                    <td className="mono small">{cert.domains.join(', ')}</td>
                    <td>
                      {cached ? (
                        <>
                          <span className={`badge ${expired ? 'bad' : daysLeft !== null && daysLeft < 30 ? 'warn' : 'ok'}`}>
                            {expired ? 'expired' : `${daysLeft}d left`}
                          </span>
                          <div className="muted small">
                            {cached.issuer} · {expires?.toLocaleDateString()}
                            {cert.staging ? ' · staging' : ''}
                          </div>
                        </>
                      ) : (
                        <span className="badge warn">not requested</span>
                      )}
                    </td>
                    <td className="muted">{cert.usage}</td>
                    <td className="actions">
                      <button onClick={() => issue(cert, 'request')} disabled={busy === cert.id}>
                        {busy === cert.id ? 'Working…' : 'Request'}
                      </button>{' '}
                      <button onClick={() => issue(cert, 'renew')} disabled={busy === cert.id || !cached}>
                        Renew
                      </button>{' '}
                      <button onClick={() => setEditing(cert)}>Edit</button>{' '}
                      <button className="danger" onClick={() => remove(cert)}>
                        Delete
                      </button>
                    </td>
                  </tr>
                )
              })}
              {!list.data?.length && (
                <tr>
                  <td colSpan={5} className="muted">
                    No certificate profiles yet. Create one to serve HTTPS on the phishing listener.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <CertificateEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            list.reload()
          }}
        />
      )}
    </>
  )
}

function blankCertificate(): Certificate {
  return {
    id: 0,
    name: '',
    domains: [],
    email: '',
    staging: false,
    accept_tos: false,
    cache_dir: '',
    http_addr: '',
    usage: 0,
    status: { certificates: [], renews_at: null, cache_dir: '', http_addr: '' },
  }
}

function CertificateEditor({
  value,
  onClose,
  onSaved,
}: {
  value: Certificate
  onClose: () => void
  onSaved: () => void
}) {
  const [cert, setCert] = useState(value)
  const [domains, setDomains] = useState(value.domains.join(', '))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const set = (patch: Partial<Certificate>) => setCert({ ...cert, ...patch })

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const body = {
        name: cert.name,
        domains: domains.split(/[\s,]+/).filter(Boolean),
        email: cert.email,
        staging: cert.staging,
        accept_tos: cert.accept_tos,
        cache_dir: cert.cache_dir,
        http_addr: cert.http_addr,
      }
      if (cert.id) await api.put(`/api/v1/certificates/${cert.id}`, body)
      else await api.post('/api/v1/certificates', body)
      onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={cert.id ? 'Edit certificate' : 'New certificate'} onClose={onClose}>
      <ErrorBox message={error} />
      <form onSubmit={save}>
        <Field label="Name">
          <input value={cert.name} onChange={(e) => set({ name: e.target.value })} required autoFocus />
        </Field>
        <Field
          label="Domains"
          hint="Comma or space separated. Every name must point at this server and be reachable on port 80 for the challenge."
        >
          <input value={domains} onChange={(e) => setDomains(e.target.value)} placeholder="training.example.com, www.training.example.com" />
        </Field>
        <Field label="Contact email" hint="Let's Encrypt sends expiry warnings here.">
          <input value={cert.email} onChange={(e) => set({ email: e.target.value })} />
        </Field>
        <div className="row">
          <div style={{ flex: 1 }}>
            <Field label="Certificate cache" hint="Defaults to data/acme.">
              <input value={cert.cache_dir} onChange={(e) => set({ cache_dir: e.target.value })} placeholder="data/acme" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Challenge address" hint="Defaults to :80.">
              <input value={cert.http_addr} onChange={(e) => set({ http_addr: e.target.value })} placeholder=":80" />
            </Field>
          </div>
        </div>
        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={cert.accept_tos}
            onChange={(e) => set({ accept_tos: e.target.checked })}
          />
          Accept the Let's Encrypt terms of service
        </label>
        <div style={{ height: 8 }} />
        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={cert.staging}
            onChange={(e) => set({ staging: e.target.checked })}
          />
          Use the staging CA (untrusted, for testing)
        </label>

        <div className="row" style={{ justifyContent: 'flex-end', marginTop: 16 }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
      {cert.id > 0 && cert.status.certificates?.[0] && (
        <p className="hint" style={{ marginBottom: 0 }}>
          Cached certificate for {cert.status.certificates[0].domain}, renews around{' '}
          {cert.status.renews_at ? timeAgo(cert.status.renews_at) : 'unknown'}.
        </p>
      )}
    </Modal>
  )
}
