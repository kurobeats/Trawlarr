import { useState } from 'react'
import { api, errorText } from '../api'
import { DraftPanel, useAIStatus } from '../ai'
import { ErrorBox, Field, Loading, Modal, useAsync } from '../ui'

type Named = { id: number; name: string }

function useCrud<T extends Named>(base: string) {
  const list = useAsync(() => api.get<T[]>(base))
  const [error, setError] = useState('')
  async function save(item: unknown, id?: number) {
    setError('')
    try {
      if (id) await api.put(`${base}/${id}`, item)
      else await api.post(base, item)
      list.reload()
      return true
    } catch (err) {
      setError(errorText(err))
      return false
    }
  }
  async function remove(id: number, label: string) {
    if (!window.confirm(`Delete ${label}?`)) return
    setError('')
    try {
      await api.del(`${base}/${id}`)
      list.reload()
    } catch (err) {
      setError(errorText(err))
    }
  }
  return { list, save, remove, error }
}

function ListShell({
  title,
  onNew,
  error,
  loading,
  children,
}: {
  title: string
  onNew: () => void
  error: string
  loading: boolean
  children: React.ReactNode
}) {
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>{title}</h1>
        <div className="grow" />
        <button className="primary" onClick={onNew}>
          New
        </button>
      </div>
      <ErrorBox message={error} />
      {loading ? <Loading /> : <div className="card">{children}</div>}
    </>
  )
}

// ------------------------------------------------------------------ templates

type Attachment = { id: number; name: string; size: number; content_type: string }
type Template = Named & {
  subject: string
  text_body: string
  html_body: string
  attachments: number[]
}

export function Templates() {
  const crud = useCrud<Template>('/api/v1/templates')
  const attachments = useAsync(() => api.get<Attachment[]>('/api/v1/attachments'))
  const [editing, setEditing] = useState<Template | null>(null)
  const [preview, setPreview] = useState<Template | null>(null)
  const blank: Template = { id: 0, name: '', subject: '', text_body: '', html_body: '', attachments: [] }

  return (
    <ListShell
      title="Email templates"
      onNew={() => setEditing({ ...blank })}
      error={crud.error}
      loading={crud.list.loading}
    >
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Subject</th>
            <th>Attachments</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {crud.list.data?.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td>
              <td className="muted">{t.subject}</td>
              <td className="muted">{t.attachments?.length ?? 0}</td>
              <td className="actions">
                <button onClick={() => setPreview(t)}>Preview</button>{' '}
                <button onClick={() => setEditing(t)}>Edit</button>{' '}
                <button className="danger" onClick={() => crud.remove(t.id, t.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {!crud.list.data?.length && (
            <tr>
              <td colSpan={4} className="muted">
                No templates yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {editing && (
        <TemplateEditor
          value={editing}
          attachments={attachments.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={async (t) => {
            if (await crud.save(t, t.id || undefined)) setEditing(null)
          }}
          onUpload={async (file) => {
            await api.upload('/api/v1/attachments', file)
            attachments.reload()
          }}
        />
      )}
      {preview && <TemplatePreview template={preview} onClose={() => setPreview(null)} />}
    </ListShell>
  )
}

type RenderedEmail = {
  subject: string
  text_body: string
  html_body: string
  attachments: string[]
}

// TemplatePreview shows the email the way a recipient would see it, with
// sample values substituted and every tracking URL made inert.
function TemplatePreview({ template, onClose }: { template: Template; onClose: () => void }) {
  const [mode, setMode] = useState<'html' | 'text'>('html')
  const { data, error, loading } = useAsync(
    () => api.get<RenderedEmail>(`/api/v1/templates/${template.id}/preview`),
    [template.id],
  )
  const hasHTML = Boolean(data?.html_body)
  const view = !hasHTML ? 'text' : mode

  return (
    <Modal title={`Preview: ${template.name}`} onClose={onClose} wide>
      <ErrorBox message={error} />
      {loading && <Loading what="Rendering" />}
      {data && (
        <>
          <div className="card" style={{ background: 'var(--panel-2)', marginBottom: 12 }}>
            <div className="muted small">Subject</div>
            <div style={{ fontWeight: 600 }}>{data.subject || '(no subject)'}</div>
            {data.attachments.length > 0 && (
              <div className="muted small" style={{ marginTop: 6 }}>
                Attachments: {data.attachments.join(', ')}
              </div>
            )}
          </div>

          {hasHTML && (
            <div className="row" style={{ marginBottom: 8 }}>
              <button className={view === 'html' ? 'primary' : ''} onClick={() => setMode('html')}>
                HTML
              </button>
              <button className={view === 'text' ? 'primary' : ''} onClick={() => setMode('text')}>
                Plain text
              </button>
            </div>
          )}

          {view === 'html' ? (
            <iframe
              title={`Preview of ${template.name}`}
              srcDoc={data.html_body}
              // No scripts, no forms, no same-origin access.
              sandbox=""
              style={{
                width: '100%',
                height: '55vh',
                border: '1px solid var(--line)',
                borderRadius: 8,
                background: '#fff',
              }}
            />
          ) : (
            <pre
              className="prewrap"
              style={{
                background: 'var(--panel-2)',
                padding: 12,
                borderRadius: 8,
                margin: 0,
                maxHeight: '55vh',
                overflow: 'auto',
              }}
            >
              {data.text_body || '(no plain-text body)'}
            </pre>
          )}

          <p className="hint" style={{ marginBottom: 0 }}>
            Sample values. Links point nowhere and the tracking pixel is inline, so opening a preview makes no
            requests to the phishing listener.
          </p>
        </>
      )}
    </Modal>
  )
}

function TemplateEditor({
  value,
  attachments,
  onClose,
  onSaved,
  onUpload,
}: {
  value: Template
  attachments: Attachment[]
  onClose: () => void
  onSaved: (t: Template) => void
  onUpload: (f: File) => Promise<void>
}) {
  const [t, setT] = useState(value)
  const [error, setError] = useState('')
  const ai = useAIStatus()
  const set = (patch: Partial<Template>) => setT({ ...t, ...patch })
  const toggle = (id: number) =>
    set({
      attachments: t.attachments?.includes(id)
        ? t.attachments.filter((a) => a !== id)
        : [...(t.attachments ?? []), id],
    })

  return (
    <Modal title={value.id ? 'Edit template' : 'New template'} onClose={onClose} wide>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          onSaved(t)
        }}
      >
        <ErrorBox message={error} />
        {ai.enabled && (
          <DraftPanel
            label="Draft with AI"
            onDraft={async (prompt) => {
              const draft = await api.post<{ subject: string; text_body: string; html_body: string }>(
                '/api/v1/ai/generate',
                { kind: 'email', ...prompt },
              )
              set({
                subject: draft.subject || t.subject,
                text_body: draft.text_body || t.text_body,
                html_body: draft.html_body || t.html_body,
              })
            }}
          />
        )}
        <Field label="Import an existing email" hint="Paste or pick a .eml; links point at the tracked URL.">
          <input
            type="file"
            accept=".eml,message/rfc822"
            onChange={async (e) => {
              const file = e.target.files?.[0]
              if (!file) return
              const content = await file.text()
              try {
                const draft = await api.post<{ subject: string; text_body: string; html_body: string }>(
                  '/api/v1/templates/import-email',
                  { content, convert_links: true },
                )
                set({
                  subject: draft.subject || t.subject,
                  text_body: draft.text_body || t.text_body,
                  html_body: draft.html_body || t.html_body,
                })
              } catch (err) {
                setError(errorText(err))
              }
            }}
          />
        </Field>
        <Field label="Template name">
          <input value={t.name} onChange={(e) => set({ name: e.target.value })} required autoFocus />
        </Field>
        <Field label="Subject" hint="Placeholders: {{.FirstName}} {{.LastName}} {{.Position}} {{.Email}}">
          <input value={t.subject} onChange={(e) => set({ subject: e.target.value })} />
        </Field>
        <Field label="Plain text body">
          <textarea value={t.text_body} onChange={(e) => set({ text_body: e.target.value })} />
        </Field>
        <Field
          label="HTML body"
          hint="Include {{.Tracker}} for open tracking and {{.URL}} for the tracked link. {{.OptOutURL}} adds an opt-out link."
        >
          <textarea value={t.html_body} onChange={(e) => set({ html_body: e.target.value })} style={{ minHeight: 200 }} />
        </Field>
        <Field label="Attachments">
          <div className="row">
            {attachments.map((a) => (
              <span
                key={a.id}
                className={`chip${t.attachments?.includes(a.id) ? ' on' : ''}`}
                onClick={() => toggle(a.id)}
              >
                {a.name}
              </span>
            ))}
            {!attachments.length && <span className="muted small">No uploads yet.</span>}
          </div>
          <input
            type="file"
            style={{ marginTop: 8 }}
            onChange={async (e) => {
              const f = e.target.files?.[0]
              if (f) await onUpload(f)
            }}
          />
        </Field>
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Save
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------- pages

type Page = Named & {
  html: string
  capture: boolean
  capture_passwords: boolean
  capture_values: boolean
  redirect_url: string
}

export function Pages() {
  const crud = useCrud<Page>('/api/v1/pages')
  const [editing, setEditing] = useState<Page | null>(null)
  const [preview, setPreview] = useState<Page | null>(null)

  return (
    <ListShell
      title="Landing pages"
      onNew={() =>
        setEditing({ id: 0, name: '', html: '<html>\n<body>\n<form>\n  <input name="username" />\n  <input name="password" type="password" />\n  <button>Sign in</button>\n</form>\n</body>\n</html>', capture: true, capture_passwords: false, capture_values: false, redirect_url: '' })
      }
      error={crud.error}
      loading={crud.list.loading}
    >
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Captures</th>
            <th>Redirect</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {crud.list.data?.map((p) => (
            <tr key={p.id}>
              <td>{p.name}</td>
              <td className="muted">
                {p.capture ? (p.capture_values ? 'field values' : 'field presence') : 'nothing'}
                {p.capture_passwords ? ' + passwords' : ''}
              </td>
              <td className="muted small">{p.redirect_url || '—'}</td>
              <td className="actions">
                <button onClick={() => setPreview(p)}>Preview</button>{' '}
                <button onClick={() => setEditing(p)}>Edit</button>{' '}
                <button className="danger" onClick={() => crud.remove(p.id, p.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {!crud.list.data?.length && (
            <tr>
              <td colSpan={4} className="muted">
                No landing pages yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {editing && (
        <PageEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={async (p) => {
            if (await crud.save(p, p.id || undefined)) setEditing(null)
          }}
        />
      )}
      {preview && (
        <Modal title={`Preview: ${preview.name}`} onClose={() => setPreview(null)} wide>
          <iframe
            title={`Preview of ${preview.name}`}
            src={`/api/v1/pages/${preview.id}/preview`}
            // Strictest sandbox: no scripts, no forms, no same-origin access.
            sandbox=""
            style={{
              width: '100%',
              height: '68vh',
              border: '1px solid var(--line)',
              borderRadius: 8,
              background: '#fff',
            }}
          />
          <div className="row" style={{ justifyContent: 'space-between', marginTop: 10 }}>
            <span className="hint">
              Rendered with sample values. Scripts and form submission are disabled in the preview.
            </span>
            <a className="badge info" href={`/api/v1/pages/${preview.id}/preview`} target="_blank" rel="noreferrer" style={{ padding: '6px 12px' }}>
              Open in a new tab
            </a>
          </div>
        </Modal>
      )}
    </ListShell>
  )
}

function PageEditor({ value, onClose, onSaved }: { value: Page; onClose: () => void; onSaved: (p: Page) => void }) {
  const [p, setP] = useState(value)
  const ai = useAIStatus()
  const [importURL, setImportURL] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function importPage() {
    setBusy(true)
    setError('')
    try {
      const res = await api.post<{ html: string }>('/api/v1/pages/import-url', { url: importURL })
      setP({ ...p, html: res.html })
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={value.id ? 'Edit landing page' : 'New landing page'} onClose={onClose} wide>
      <ErrorBox message={error} />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          onSaved(p)
        }}
      >
        {ai.enabled && (
          <DraftPanel
            label="Draft with AI"
            onDraft={async (prompt) => {
              const draft = await api.post<{ name: string; html: string }>('/api/v1/ai/generate', {
                kind: 'landing_page',
                ...prompt,
              })
              setP({ ...p, html: draft.html || p.html, name: draft.name || p.name })
            }}
          />
        )}
        <Field label="Page name">
          <input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} required autoFocus />
        </Field>
        <Field
          label="Clone from URL"
          hint="Imports a live page and rewrites its assets to absolute URLs. https:// is assumed; http:// is tried if that fails."
        >
          <div className="row">
            <input
              style={{ flex: 1 }}
              value={importURL}
              onChange={(e) => setImportURL(e.target.value)}
              placeholder="https://login.example.com"
            />
            <button type="button" onClick={importPage} disabled={busy || !importURL}>
              {busy ? 'Importing…' : 'Import'}
            </button>
          </div>
        </Field>
        <Field label="HTML" hint="Forms are automatically pointed at the capture endpoint. {{.FirstName}} and friends work here too.">
          <textarea value={p.html} onChange={(e) => setP({ ...p, html: e.target.value })} style={{ minHeight: 260 }} />
        </Field>
        <div className="row">
          <label className="chip">
            <input
              type="checkbox"
              style={{ width: 'auto' }}
              checked={p.capture}
              onChange={(e) => setP({ ...p, capture: e.target.checked })}
            />
            Capture submitted form data
          </label>
          <label className="chip">
            <input
              type="checkbox"
              style={{ width: 'auto' }}
              checked={p.capture_passwords}
              onChange={(e) => setP({ ...p, capture_passwords: e.target.checked })}
            />
            Include password fields
          </label>
          <label className="chip">
            <input
              type="checkbox"
              style={{ width: 'auto' }}
              checked={p.capture_values}
              onChange={(e) => setP({ ...p, capture_values: e.target.checked })}
            />
            Store the values that were typed
          </label>
        </div>
        <p className="hint">
          Off by default: the report records which fields were filled, never their contents. Only enable value storage
          if your engagement rules require it, and delete the campaign data afterwards.
        </p>
        <div style={{ height: 12 }} />
        <Field label="Redirect after submit" hint="Leave blank to show the built-in training page.">
          <input value={p.redirect_url} onChange={(e) => setP({ ...p, redirect_url: e.target.value })} />
        </Field>
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Save
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ------------------------------------------------------------------- profiles

type Header = { key: string; value: string }
type Profile = Named & {
  host: string
  port: number
  username: string
  password?: string
  from_address: string
  envelope_sender: string
  ignore_cert: boolean
  headers: Header[]
  has_password: boolean
  last_test_at: string | null
  last_test_error: string
}

export function Profiles() {
  const crud = useCrud<Profile>('/api/v1/profiles')
  const [editing, setEditing] = useState<Profile | null>(null)

  return (
    <ListShell
      title="Sending profiles"
      onNew={() =>
        setEditing({
          id: 0,
          name: '',
          host: '',
          port: 587,
          username: '',
          password: '',
          from_address: '',
          envelope_sender: '',
          ignore_cert: false,
          headers: [],
          has_password: false,
          last_test_at: null,
          last_test_error: '',
        })
      }
      error={crud.error}
      loading={crud.list.loading}
    >
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Server</th>
            <th>From</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {crud.list.data?.map((p) => (
            <tr key={p.id}>
              <td>{p.name}</td>
              <td className="muted">
                {p.host}:{p.port}
              </td>
              <td className="muted">
                {p.from_address}
                {p.last_test_error ? (
                  <div className="small" style={{ color: 'var(--bad)' }}>last test failed</div>
                ) : (
                  <div className="small" style={{ color: 'var(--ok)' }}>{p.last_test_at ? 'tested' : 'untested'}</div>
                )}
              </td>
              <td className="actions">
                <button onClick={() => setEditing(p)}>Edit</button>{' '}
                <button className="danger" onClick={() => crud.remove(p.id, p.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {!crud.list.data?.length && (
            <tr>
              <td colSpan={4} className="muted">
                No sending profiles yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {editing && (
        <ProfileEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={async (p) => {
            if (await crud.save(p, p.id || undefined)) setEditing(null)
          }}
        />
      )}
    </ListShell>
  )
}

function ProfileEditor({
  value,
  onClose,
  onSaved,
}: {
  value: Profile
  onClose: () => void
  onSaved: (p: Profile) => void
}) {
  const [p, setP] = useState(value)
  const [testTo, setTestTo] = useState('')
  const [note, setNote] = useState('')

  async function sendTest() {
    setNote('')
    try {
      await api.post(`/api/v1/profiles/${p.id}/test`, { to: testTo })
      setNote('Test message accepted by the relay.')
    } catch (err) {
      setNote(errorText(err))
    }
  }

  return (
    <Modal title={value.id ? 'Edit sending profile' : 'New sending profile'} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          onSaved(p)
        }}
      >
        <Field label="Profile name">
          <input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} required autoFocus />
        </Field>
        <div className="row">
          <div style={{ flex: 2 }}>
            <Field label="SMTP host">
              <input value={p.host} onChange={(e) => setP({ ...p, host: e.target.value })} placeholder="smtp.example.com" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Port">
              <input type="number" value={p.port} onChange={(e) => setP({ ...p, port: Number(e.target.value) })} />
            </Field>
          </div>
        </div>
        <div className="row">
          <div style={{ flex: 1 }}>
            <Field label="Username">
              <input value={p.username} onChange={(e) => setP({ ...p, username: e.target.value })} autoComplete="off" />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label="Password" hint={p.has_password ? 'Stored. Leave blank to keep it.' : 'Stored encrypted.'}>
              <input
                type="password"
                value={p.password ?? ''}
                onChange={(e) => setP({ ...p, password: e.target.value })}
                autoComplete="new-password"
              />
            </Field>
          </div>
        </div>
        <Field label="From address" hint="Name <address@example.com>. The domain drives the HELO and Message-ID.">
          <input value={p.from_address} onChange={(e) => setP({ ...p, from_address: e.target.value })} />
        </Field>
        <Field label="Envelope sender" hint="Optional MAIL FROM override, for relays that need a different bounce address.">
          <input value={p.envelope_sender} onChange={(e) => setP({ ...p, envelope_sender: e.target.value })} />
        </Field>
        <Field label="Custom headers">
          {p.headers.map((h, i) => (
            <div className="row" key={i} style={{ marginBottom: 6 }}>
              <input
                style={{ flex: 1 }}
                placeholder="X-Header"
                value={h.key}
                onChange={(e) => {
                  const headers = [...p.headers]
                  headers[i] = { ...h, key: e.target.value }
                  setP({ ...p, headers })
                }}
              />
              <input
                style={{ flex: 2 }}
                placeholder="value"
                value={h.value}
                onChange={(e) => {
                  const headers = [...p.headers]
                  headers[i] = { ...h, value: e.target.value }
                  setP({ ...p, headers })
                }}
              />
              <button type="button" onClick={() => setP({ ...p, headers: p.headers.filter((_, j) => j !== i) })}>
                ×
              </button>
            </div>
          ))}
          <button type="button" onClick={() => setP({ ...p, headers: [...p.headers, { key: '', value: '' }] })}>
            Add header
          </button>
        </Field>
        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={p.ignore_cert}
            onChange={(e) => setP({ ...p, ignore_cert: e.target.checked })}
          />
          Ignore TLS certificate errors
        </label>
        <div className="row" style={{ justifyContent: 'flex-end', marginTop: 12 }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Save
          </button>
        </div>
      </form>
      {p.id > 0 && (
        <div style={{ marginTop: 16, borderTop: '1px solid var(--line)', paddingTop: 12 }}>
          <Field label="Send a test email">
            <div className="row">
              <input style={{ flex: 1 }} value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="you@example.com" />
              <button type="button" onClick={sendTest} disabled={!testTo}>
                Send test
              </button>
            </div>
          </Field>
          {note && <div className="hint">{note}</div>}
        </div>
      )}
    </Modal>
  )
}

// --------------------------------------------------------------------- groups

type Target = { email: string; first_name: string; last_name: string; position: string; custom?: Record<string, string> }
type Group = Named & { targets: Target[] }

export function Groups() {
  const crud = useCrud<Group>('/api/v1/groups')
  const [editing, setEditing] = useState<Group | null>(null)

  return (
    <ListShell
      title="Recipient groups"
      onNew={() => setEditing({ id: 0, name: '', targets: [] })}
      error={crud.error}
      loading={crud.list.loading}
    >
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Recipients</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {crud.list.data?.map((g) => (
            <tr key={g.id}>
              <td>{g.name}</td>
              <td className="muted">{g.targets?.length ?? 0}</td>
              <td className="actions">
                <button onClick={() => setEditing(g)}>Edit</button>{' '}
                <button className="danger" onClick={() => crud.remove(g.id, g.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {!crud.list.data?.length && (
            <tr>
              <td colSpan={3} className="muted">
                No groups yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {editing && (
        <GroupEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={async (g) => {
            if (await crud.save(g, g.id || undefined)) setEditing(null)
          }}
        />
      )}
    </ListShell>
  )
}

function GroupEditor({ value, onClose, onSaved }: { value: Group; onClose: () => void; onSaved: (g: Group) => void }) {
  const [g, setG] = useState(value)
  const [error, setError] = useState('')
  const [csv, setCsv] = useState<{ count: number; targets: Target[] } | null>(null)

  function update(i: number, patch: Partial<Target>) {
    const targets = [...g.targets]
    targets[i] = { ...targets[i], ...patch }
    setG({ ...g, targets })
  }

  async function importCSV(file: File) {
    setError('')
    try {
      const res = await api.upload<{ count: number; targets: Target[] }>('/api/v1/groups/import', file)
      setCsv(res)
    } catch (err) {
      setError(errorText(err))
    }
  }

  return (
    <Modal title={value.id ? 'Edit group' : 'New group'} onClose={onClose} wide>
      <ErrorBox message={error} />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          onSaved({ ...g, targets: g.targets.filter((t) => t.email) })
        }}
      >
        <Field label="Group name">
          <input value={g.name} onChange={(e) => setG({ ...g, name: e.target.value })} required autoFocus />
        </Field>
        <Field
          label="Import CSV"
          hint="Header row optional. Recognised columns: email, first_name, last_name, position; anything else becomes a custom field."
        >
          <div className="row">
            <input type="file" accept=".csv,text/csv" onChange={(e) => e.target.files?.[0] && importCSV(e.target.files[0])} />
            {csv && (
              <button
                type="button"
                onClick={() => {
                  setG({ ...g, targets: [...g.targets, ...csv.targets] })
                  setCsv(null)
                }}
              >
                Add {csv.count} parsed recipients
              </button>
            )}
          </div>
        </Field>
        <table>
          <thead>
            <tr>
              <th>Email</th>
              <th>First name</th>
              <th>Last name</th>
              <th>Position</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {g.targets.map((t, i) => (
              <tr key={i}>
                <td>
                  <input value={t.email} onChange={(e) => update(i, { email: e.target.value })} />
                </td>
                <td>
                  <input value={t.first_name} onChange={(e) => update(i, { first_name: e.target.value })} />
                </td>
                <td>
                  <input value={t.last_name} onChange={(e) => update(i, { last_name: e.target.value })} />
                </td>
                <td>
                  <input value={t.position} onChange={(e) => update(i, { position: e.target.value })} />
                </td>
                <td className="actions">
                  <button type="button" onClick={() => setG({ ...g, targets: g.targets.filter((_, j) => j !== i) })}>
                    ×
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <button
          type="button"
          style={{ marginTop: 10 }}
          onClick={() => setG({ ...g, targets: [...g.targets, { email: '', first_name: '', last_name: '', position: '' }] })}
        >
          Add recipient
        </button>
        <div className="row" style={{ justifyContent: 'flex-end', marginTop: 16 }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Save
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ------------------------------------------------------------------- webhooks

type Webhook = Named & { url: string; secret: string; events: string[]; enabled: boolean; has_secret: boolean }

export function WebhooksPage() {
  const settings = useAsync(() => api.get<{ events: string[] }>('/api/v1/settings'))
  if (settings.loading) return <Loading />
  return (
    <>
      <ErrorBox message={settings.error} />
      <Webhooks events={settings.data?.events ?? []} />
    </>
  )
}

export function Webhooks({ events }: { events: string[] }) {
  const crud = useCrud<Webhook>('/api/v1/webhooks')
  const [editing, setEditing] = useState<Webhook | null>(null)

  return (
    <ListShell
      title="Webhooks"
      onNew={() => setEditing({ id: 0, name: '', url: '', secret: '', events: ['*'], enabled: true, has_secret: false })}
      error={crud.error}
      loading={crud.list.loading}
    >
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>URL</th>
            <th>Events</th>
            <th>State</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {crud.list.data?.map((w) => (
            <tr key={w.id}>
              <td>{w.name}</td>
              <td className="mono muted">{w.url}</td>
              <td className="muted small">{w.events.join(', ')}</td>
              <td>{w.enabled ? <span className="badge ok">enabled</span> : <span className="badge">disabled</span>}</td>
              <td className="actions">
                <button onClick={() => setEditing(w)}>Edit</button>{' '}
                <button className="danger" onClick={() => crud.remove(w.id, w.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {!crud.list.data?.length && (
            <tr>
              <td colSpan={5} className="muted">
                No webhooks yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {editing && (
        <WebhookEditor
          value={editing}
          events={events}
          onClose={() => setEditing(null)}
          onSaved={async (w) => {
            if (await crud.save(w, w.id || undefined)) setEditing(null)
          }}
        />
      )}
    </ListShell>
  )
}

function WebhookEditor({
  value,
  events,
  onClose,
  onSaved,
}: {
  value: Webhook
  events: string[]
  onClose: () => void
  onSaved: (w: Webhook) => void
}) {
  const [w, setW] = useState(value)
  const toggle = (e: string) =>
    setW({ ...w, events: w.events.includes(e) ? w.events.filter((x) => x !== e) : [...w.events, e] })

  return (
    <Modal title={value.id ? 'Edit webhook' : 'New webhook'} onClose={onClose}>
      <form
        onSubmit={(ev) => {
          ev.preventDefault()
          onSaved(w)
        }}
      >
        <Field label="Name">
          <input value={w.name} onChange={(e) => setW({ ...w, name: e.target.value })} autoFocus />
        </Field>
        <Field label="Endpoint URL">
          <input value={w.url} onChange={(e) => setW({ ...w, url: e.target.value })} required />
        </Field>
        <Field
          label="Signing secret"
          hint={
            w.has_secret
              ? 'Stored. Leave blank to keep it. Requests carry X-Webhook-Signature: sha256=HMAC(body).'
              : 'Requests carry X-Webhook-Signature: sha256=HMAC(body).'
          }
        >
          <input value={w.secret} onChange={(e) => setW({ ...w, secret: e.target.value })} />
        </Field>
        <Field label="Events">
          <div className="row">
            {events.map((e) => (
              <span key={e} className={`chip${w.events.includes(e) ? ' on' : ''}`} onClick={() => toggle(e)}>
                {e}
              </span>
            ))}
            <span className={`chip${w.events.includes('*') ? ' on' : ''}`} onClick={() => toggle('*')}>
              all
            </span>
          </div>
        </Field>
        <label className="chip">
          <input
            type="checkbox"
            style={{ width: 'auto' }}
            checked={w.enabled}
            onChange={(e) => setW({ ...w, enabled: e.target.checked })}
          />
          Enabled
        </label>
        <div className="row" style={{ justifyContent: 'flex-end', marginTop: 12 }}>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Save
          </button>
        </div>
      </form>
    </Modal>
  )
}
