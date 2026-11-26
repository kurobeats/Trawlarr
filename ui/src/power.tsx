import { useEffect, useState } from 'react'
import { api, errorText } from './api'

type State = 'idle' | 'restarting' | 'stopped'

// PowerMenu is the sidebar control for stopping or restarting the process.
// It renders its own full-screen overlay while the server is away.
export function PowerMenu() {
  const [open, setOpen] = useState(false)
  const [state, setState] = useState<State>('idle')
  const [error, setError] = useState('')

  async function act(action: 'restart' | 'shutdown') {
    const question =
      action === 'restart'
        ? 'Restart the server now? Campaigns that were sending resume automatically.'
        : 'Shut the server down now? The admin UI and the tracking listener stop immediately.'
    if (!window.confirm(question)) return
    setError('')
    try {
      await api.post(`/api/v1/power/${action}`)
      setOpen(false)
      setState(action === 'restart' ? 'restarting' : 'stopped')
    } catch (err) {
      setError(errorText(err))
    }
  }

  // Poll until the restarted server answers, then reload the UI.
  useEffect(() => {
    if (state !== 'restarting') return
    const timer = window.setInterval(async () => {
      try {
        await api.get('/api/v1/health')
        window.location.reload()
      } catch {
        // still starting
      }
    }, 1500)
    return () => window.clearInterval(timer)
  }, [state])

  return (
    <>
      <div
        className={`nav-item${open ? ' active' : ''}`}
        onClick={() => setOpen(!open)}
        style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}
      >
        <span>Power</span>
        <span className="muted small">{open ? '▾' : '▸'}</span>
      </div>
      {open && (
        <>
          <div className="nav-sub" onClick={() => act('restart')}>
            Restart server
          </div>
          <div className="nav-sub danger" onClick={() => act('shutdown')}>
            Shut down server
          </div>
        </>
      )}
      {error && (
        <div className="small" style={{ color: 'var(--bad)', padding: '4px 10px' }}>
          {error}
        </div>
      )}

      {state !== 'idle' && (
        <div className="power-overlay">
          <div className="card" style={{ width: 'min(420px, 92vw)' }}>
            <h2 style={{ marginTop: 0 }}>{state === 'restarting' ? 'Restarting…' : 'Server stopped'}</h2>
            {state === 'restarting' ? (
              <>
                <p className="muted">
                  The server is starting again. This page reloads as soon as it answers.
                </p>
                <div className="bar">
                  <span style={{ width: '40%' }} />
                </div>
              </>
            ) : (
              <>
                <p className="muted">
                  The admin UI and the tracking listener are down. Start the service again when you are ready.
                </p>
                <button onClick={() => setState('idle')}>Back to the UI</button>
              </>
            )}
          </div>
        </div>
      )}
    </>
  )
}
