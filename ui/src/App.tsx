import { useCallback, useEffect, useState } from 'react'
import { api, errorText } from './api'
import { Loading, useHashRoute } from './ui'
import { Login } from './pages/Login'
import { Dashboard } from './pages/Dashboard'
import { Campaigns, CampaignDetail } from './pages/Campaigns'
import { Templates, Pages, Profiles, Groups, WebhooksPage } from './pages/Library'
import { Settings } from './pages/Settings'

export type Me = {
  id: number
  username: string
  email: string
  role: 'admin' | 'operator' | 'viewer'
  role_rank: number
  totp_enabled: boolean
  has_api_key: boolean
  org_name: string
  org_id: number
  phish_url: string
}

const NAV = [
  ['/', 'Dashboard'],
  ['/campaigns', 'Campaigns'],
  ['/templates', 'Templates'],
  ['/pages', 'Landing Pages'],
  ['/profiles', 'Sending Profiles'],
  ['/groups', 'Groups'],
  ['/webhooks', 'Webhooks'],
  ['/settings', 'Settings'],
]

export function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [booting, setBooting] = useState(true)
  const [route, navigate] = useHashRoute()

  useEffect(() => {
    api
      .get<Me>('/api/v1/me')
      .then(setMe)
      .catch(() => setMe(null))
      .finally(() => setBooting(false))
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.post('/api/v1/logout')
    } finally {
      setMe(null)
    }
  }, [])

  if (booting) return <div className="login-wrap"><Loading what="Starting" /></div>
  if (!me) return <Login onLogin={setMe} />

  const active = (prefix: string) =>
    prefix === '/' ? route === '/' : route === prefix || route.startsWith(prefix + '/')

  let page = <Dashboard navigate={navigate} />
  if (route.startsWith('/campaigns/')) {
    page = <CampaignDetail id={Number(route.split('/')[2])} navigate={navigate} />
  } else if (active('/campaigns')) {
    page = <Campaigns navigate={navigate} />
  } else if (active('/templates')) {
    page = <Templates />
  } else if (active('/pages')) {
    page = <Pages />
  } else if (active('/profiles')) {
    page = <Profiles />
  } else if (active('/groups')) {
    page = <Groups />
  } else if (active('/webhooks')) {
    page = <WebhooksPage />
  } else if (active('/settings')) {
    page = <Settings me={me} onMe={setMe} />
  }

  return (
    <div className="layout">
      <aside className="sidebar">
        <div className="brand">
          <span className="dot" /> Trawlarr
        </div>
        {NAV.map(([href, label]) => (
          <a
            key={href}
            href={'#' + href}
            className={`nav-item${active(href) ? ' active' : ''}`}
            onClick={() => navigate(href)}
          >
            {label}
          </a>
        ))}
        <div className="spacer" />
        <div className="muted small" style={{ padding: '8px 10px' }}>
          {me.phish_url}
        </div>
      </aside>
      <div className="main">
        <div className="topbar">
          <strong>{me.org_name}</strong>
          <div className="right">
            <span className="small">
              {me.username} · {me.role}
            </span>
            <button onClick={logout}>Sign out</button>
          </div>
        </div>
        <div className="content">{page}</div>
      </div>
    </div>
  )
}

export function useToast() {
  const [message, setMessage] = useState('')
  const [kind, setKind] = useState<'ok' | 'bad'>('ok')
  const show = useCallback((text: string, isError = false) => {
    setKind(isError ? 'bad' : 'ok')
    setMessage(text)
    window.setTimeout(() => setMessage(''), 4000)
  }, [])
  return { message, kind, show }
}

export { errorText }
