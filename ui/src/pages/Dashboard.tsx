import { api, pct, timeAgo } from '../api'
import { Badge, ErrorBox, Loading, Metric, useAsync } from '../ui'

type Stats = {
  total: number
  pending: number
  sent: number
  opened: number
  clicked: number
  submitted: number
  reported: number
  failed: number
}

type Campaign = {
  id: number
  name: string
  status: string
  created_at: string
  stats: Stats
}

type DashboardData = {
  stats: Stats
  active: number
  campaign_count: number
  object_counts: { groups: number; templates: number; pages: number; profiles: number }
  recent: Campaign[]
}

export function Dashboard({ navigate }: { navigate: (p: string) => void }) {
  const { data, error, loading } = useAsync(() => api.get<DashboardData>('/api/v1/dashboard'))
  if (loading) return <Loading />
  if (error) return <ErrorBox message={error} />
  if (!data) return null
  const s = data.stats

  return (
    <>
      <h1>Dashboard</h1>
      <p className="muted" style={{ marginTop: 0 }}>
        {data.campaign_count} campaigns · {data.active} active
      </p>

      <div className="grid cols-4" style={{ marginBottom: 14 }}>
        <Metric label="Emails sent" value={s.sent} sub={`${s.total} recipients total`} />
        <Metric label="Opened" value={pct(s.opened, s.sent)} sub={`${s.opened} recipients`} />
        <Metric label="Clicked" value={pct(s.clicked, s.sent)} sub={`${s.clicked} recipients`} />
        <Metric label="Submitted data" value={pct(s.submitted, s.sent)} sub={`${s.submitted} recipients`} />
        <Metric label="Reported" value={pct(s.reported, s.sent)} sub={`${s.reported} recipients`} />
        <Metric label="Failed" value={s.failed} sub="delivery errors" />
      </div>

      <div className="card">
        <div className="toolbar">
          <h2 style={{ margin: 0 }}>Recent campaigns</h2>
          <div className="grow" />
          <button className="primary" onClick={() => navigate('/campaigns')}>
            New campaign
          </button>
        </div>
        {data.recent.length === 0 ? (
          <p className="muted">No campaigns yet. Create a template, landing page and sending profile, then launch one.</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Sent</th>
                <th>Opened</th>
                <th>Clicked</th>
                <th>Submitted</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              {data.recent.map((c) => (
                <tr key={c.id}>
                  <td>
                    <a href={`#/campaigns/${c.id}`}>{c.name}</a>
                  </td>
                  <td>
                    <Badge status={c.status} />
                  </td>
                  <td>{c.stats.sent}</td>
                  <td>{pct(c.stats.opened, c.stats.sent)}</td>
                  <td>{pct(c.stats.clicked, c.stats.sent)}</td>
                  <td>{pct(c.stats.submitted, c.stats.sent)}</td>
                  <td className="muted small">{timeAgo(c.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="card">
        <h2>Library</h2>
        <div className="grid cols-4">
          <Metric label="Groups" value={data.object_counts.groups} />
          <Metric label="Templates" value={data.object_counts.templates} />
          <Metric label="Landing pages" value={data.object_counts.pages} />
          <Metric label="Sending profiles" value={data.object_counts.profiles} />
        </div>
      </div>
    </>
  )
}
