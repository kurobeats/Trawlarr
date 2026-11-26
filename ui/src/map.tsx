import { useEffect, useRef } from 'react'
import 'leaflet/dist/leaflet.css'

export type MapPoint = {
  email: string
  city: string
  country: string
  latitude: number
  longitude: number
  status: string
  at?: string | null
}

// CampaignMap drops one pin per geolocated recipient on OpenStreetMap tiles.
// Leaflet is imported dynamically so the map code only loads when a campaign
// actually has coordinates.
export function CampaignMap({ points }: { points: MapPoint[] }) {
  const container = useRef<HTMLDivElement | null>(null)
  const map = useRef<unknown>(null)

  useEffect(() => {
    let cancelled = false
    if (!container.current || points.length === 0) return

    import('leaflet').then((mod) => {
      const L = mod.default ?? mod
      if (cancelled || !container.current) return
      if (map.current) {
        ;(map.current as { remove: () => void }).remove()
        map.current = null
      }
      const instance = L.map(container.current, { scrollWheelZoom: false, worldCopyJump: true })
      map.current = instance
      L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
        maxZoom: 18,
        attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
      }).addTo(instance)

      const colors: Record<string, string> = {
        opened: '#a6a75d',
        clicked: '#cc8b3f',
        submitted: '#dd5e84',
        reported: '#389bc8',
      }
      points.forEach((p) => {
        L.circleMarker([p.latitude, p.longitude], {
          radius: 7,
          weight: 2,
          color: colors[p.status] ?? '#aea47a',
          fillColor: colors[p.status] ?? '#aea47a',
          fillOpacity: 0.55,
        })
          .addTo(instance)
          .bindPopup(
            `<strong>${escapeHtml(p.email)}</strong><br>${escapeHtml(p.city)}${p.city && p.country ? ', ' : ''}${escapeHtml(
              p.country,
            )}<br><span style="opacity:.7">${escapeHtml(p.status)}</span>`,
          )
      })
      instance.fitBounds(
        L.latLngBounds(points.map((p) => [p.latitude, p.longitude] as [number, number])),
        { padding: [30, 30], maxZoom: 6 },
      )
    })

    return () => {
      cancelled = true
      if (map.current) {
        ;(map.current as { remove: () => void }).remove()
        map.current = null
      }
    }
  }, [points])

  if (points.length === 0) return null
  return (
    <div
      ref={container}
      style={{ width: '100%', height: 340, borderRadius: 10, border: '1px solid var(--line)', background: 'var(--panel-2)' }}
    />
  )
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c)
}
