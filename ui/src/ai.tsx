import { useState } from 'react'
import { api, errorText } from './api'
import { Field, useAsync } from './ui'

export type AIStatus = { enabled: boolean; model?: string }

export function useAIStatus(): AIStatus {
  const { data } = useAsync(() => api.get<AIStatus>('/api/v1/ai/status'))
  return data ?? { enabled: false }
}

type Prompt = { prompt: string; tone: string; audience: string }

// DraftPanel is the shared "write this for me" control.
export function DraftPanel({
  label,
  onDraft,
}: {
  label: string
  onDraft: (prompt: Prompt) => Promise<void>
}) {
  const [open, setOpen] = useState(false)
  const [prompt, setPrompt] = useState('')
  const [tone, setTone] = useState('')
  const [audience, setAudience] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!open) {
    return (
      <button type="button" onClick={() => setOpen(true)} style={{ marginBottom: 12 }}>
        {label}
      </button>
    )
  }

  async function run() {
    setBusy(true)
    setError('')
    try {
      await onDraft({ prompt, tone, audience })
      setOpen(false)
      setPrompt('')
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card" style={{ background: 'var(--panel-2)', marginBottom: 12 }}>
      <Field label="Brief" hint="What should it say, and to whom? Placeholders are added for you.">
        <textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          style={{ minHeight: 70 }}
          placeholder="Payroll deadline reminder for the finance team"
        />
      </Field>
      <div className="row">
        <div style={{ flex: 1 }}>
          <Field label="Tone">
            <input value={tone} onChange={(e) => setTone(e.target.value)} placeholder="neutral" />
          </Field>
        </div>
        <div style={{ flex: 1 }}>
          <Field label="Audience">
            <input value={audience} onChange={(e) => setAudience(e.target.value)} placeholder="finance team" />
          </Field>
        </div>
      </div>
      {error && <div className="error">{error}</div>}
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <button type="button" onClick={() => setOpen(false)}>
          Cancel
        </button>
        <button type="button" className="primary" onClick={run} disabled={busy || !prompt.trim()}>
          {busy ? 'Drafting…' : 'Draft'}
        </button>
      </div>
    </div>
  )
}
