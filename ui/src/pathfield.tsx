import { useEffect, useId, useState } from 'react'
import { api } from './api'
import { Field } from './ui'

type Completion = { typed: string; matches: string[]; roots?: string[]; error?: string }
type Stat = {
  typed: string
  resolved?: string
  exists?: boolean
  is_dir?: boolean
  size?: number
  modified?: string
  parent_exists?: boolean
  parent_writable?: boolean
  error?: string
}

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), ms)
    return () => window.clearTimeout(timer)
  }, [value, ms])
  return debounced
}

function bytes(n?: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = n
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

// PathField is a text input with directory completion and a live preview of
// what the typed path resolves to, in the spirit of Syncthing's browse field:
// type a prefix, pick from the options, see whether it is usable before saving.
export function PathField({
  label,
  hint,
  value,
  onChange,
  placeholder,
}: {
  label: string
  hint?: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  const listId = useId()
  const [matches, setMatches] = useState<string[]>([])
  const [stat, setStat] = useState<Stat | null>(null)

  const typed = useDebounced(value, 200)
  const statTyped = useDebounced(value, 250)

  useEffect(() => {
    let alive = true
    api
      .get<Completion>(`/api/v1/fs/complete?path=${encodeURIComponent(typed)}`)
      .then((res) => alive && setMatches(typed ? res.matches : (res.roots ?? [])))
      .catch(() => alive && setMatches([]))
    return () => {
      alive = false
    }
  }, [typed])

  useEffect(() => {
    let alive = true
    if (!statTyped.trim()) {
      setStat(null)
      return
    }
    api
      .get<Stat>(`/api/v1/fs/stat?path=${encodeURIComponent(statTyped)}`)
      .then((res) => alive && setStat(res))
      .catch(() => alive && setStat(null))
    return () => {
      alive = false
    }
  }, [statTyped])

  let preview: string | null = null
  let bad = false
  if (stat) {
    bad = Boolean(stat.error)
    if (stat.error) preview = stat.error
    else if (stat.exists && !stat.is_dir)
      preview = `Existing file · ${bytes(stat.size)}${stat.modified ? ` · modified ${new Date(stat.modified).toLocaleString()}` : ''}`
    else if (!stat.exists && stat.parent_writable) preview = `New file · ${stat.resolved} · directory is writable`
    else if (!stat.exists && stat.parent_exists) preview = `New file · ${stat.resolved} · directory is not writable`
  }

  return (
    <>
      <Field label={label} hint={hint}>
        <input list={listId} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />
        <datalist id={listId}>
          {matches.map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
      </Field>
      {preview && (
        <div className="hint" style={{ marginTop: -6, marginBottom: 10, color: bad ? 'var(--bad)' : undefined }}>
          {bad ? '⚠ ' : '✓ '}
          {preview}
        </div>
      )}
    </>
  )
}

// describeListen explains what a listen address will do.
export function describeListen(raw: string): { ok: boolean; text: string; warn?: string } {
  const value = raw.trim()
  if (!value) return { ok: false, text: 'An address is required, for example 127.0.0.1:3333' }
  const withScheme = value.includes('://') ? value : `http://${value}`
  let parsed: URL
  try {
    parsed = new URL(withScheme)
  } catch {
    return { ok: false, text: 'Not a valid host:port address' }
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { ok: false, text: 'Only http or https addresses are accepted' }
  }
  if (parsed.pathname && parsed.pathname !== '/') {
    return { ok: false, text: 'A listen address cannot include a path' }
  }
  const host = parsed.hostname
  const port = parsed.port
  if (!host) return { ok: false, text: 'A host is required' }
  if (!port) return { ok: false, text: 'A port is required' }
  const n = Number(port)
  if (!Number.isInteger(n) || n < 1 || n > 65535) return { ok: false, text: 'Port must be between 1 and 65535' }

  let where = `interface ${host}`
  if (host === '127.0.0.1' || host === '::1' || host === 'localhost') where = 'loopback only — reachable from this machine'
  else if (host === '0.0.0.0' || host === '::') where = 'all interfaces — reachable from the network'

  const warn = n < 1024 ? 'Ports below 1024 need extra privileges or a capability' : undefined
  return { ok: true, text: `Listens on ${where}, port ${n}`, warn }
}

export function ListenField({
  label,
  hint,
  value,
  onChange,
  placeholder,
}: {
  label: string
  hint?: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  const described = describeListen(value)
  return (
    <>
      <Field label={label} hint={hint}>
        <input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />
      </Field>
      <div
        className="hint"
        style={{ marginTop: -6, marginBottom: 10, color: described.ok ? undefined : 'var(--bad)' }}
      >
        {described.ok ? '✓ ' : '⚠ '}
        {described.text}
        {described.warn && <span style={{ color: 'var(--warn)' }}> · {described.warn}</span>}
      </div>
    </>
  )
}
