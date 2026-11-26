import { ReactNode, useCallback, useEffect, useState } from 'react'
import { errorText } from './api'

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [nonce, setNonce] = useState(0)
  const reload = useCallback(() => setNonce((n) => n + 1), [])
  useEffect(() => {
    let alive = true
    setLoading(true)
    fn()
      .then((value) => alive && (setData(value), setError('')))
      .catch((err) => alive && setError(errorText(err)))
      .finally(() => alive && setLoading(false))
    return () => {
      alive = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce])
  return { data, error, loading, reload, setData }
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {hint && <div className="hint">{hint}</div>}
    </div>
  )
}

export function Modal({
  title,
  onClose,
  children,
  wide,
}: {
  title: string
  onClose: () => void
  children: ReactNode
  wide?: boolean
}) {
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal${wide ? ' wide' : ''}`}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="close" onClick={onClose} aria-label="close">
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  )
}

export function Badge({ status }: { status: string }) {
  const cls =
    status === 'completed' || status === 'sent' || status === 'submitted' || status === 'reported'
      ? 'ok'
      : status === 'sending' || status === 'queued' || status === 'opened' || status === 'clicked'
        ? 'info'
        : status === 'failed' || status === 'error'
          ? 'bad'
          : status === 'scheduled' || status === 'pending'
            ? 'warn'
            : ''
  return <span className={`badge ${cls}`}>{status.replace(/_/g, ' ')}</span>
}

export function Metric({ label, value, sub }: { label: string; value: ReactNode; sub?: string }) {
  return (
    <div className="metric">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {sub && <div className="sub">{sub}</div>}
    </div>
  )
}

export function ErrorBox({ message }: { message: string }) {
  if (!message) return null
  return <div className="error">{message}</div>
}

export function Loading({ what = 'Loading' }: { what?: string }) {
  return <div className="muted">{what}…</div>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="muted" style={{ padding: '12px 2px' }}>{children}</div>
}

export function useHashRoute(): [string, (path: string) => void] {
  const [route, setRoute] = useState(() => window.location.hash.replace(/^#/, '') || '/')
  useEffect(() => {
    const onHash = () => setRoute(window.location.hash.replace(/^#/, '') || '/')
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  const navigate = useCallback((path: string) => {
    window.location.hash = path
  }, [])
  return [route, navigate]
}
