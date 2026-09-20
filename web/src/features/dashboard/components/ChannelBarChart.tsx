import type { ChannelSeries } from '../types'
import { CHANNEL_COLOR } from './palette'

// Responsive SVG (viewBox scales with the container) instead of a charting
// dependency: FR1 needs two simple charts. Reassess at FR6 (Reports), where the
// real charting surface is defined.
const W = 560
const H = 200
const PAD = { top: 12, right: 8, bottom: 26, left: 30 }

const DAY_LABELS = ['Seg', 'Ter', 'Qua', 'Qui', 'Sex', 'Sáb', 'Dom']

export function ChannelBarChart({ series }: { series: ChannelSeries[] }) {
  const groups = series[0]?.buckets.length ?? 0
  if (!groups) return null

  const max = Math.max(1, ...series.flatMap((s) => s.buckets))
  const niceMax = Math.ceil(max / 5) * 5
  const plotW = W - PAD.left - PAD.right
  const plotH = H - PAD.top - PAD.bottom
  const groupW = plotW / groups
  const barW = Math.min(7, (groupW - 8) / series.length)
  const clusterW = barW * series.length

  const y = (v: number) => PAD.top + plotH - (v / niceMax) * plotH

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      width="100%"
      height="100%"
      role="img"
      aria-label={`Conversas por canal: ${series.map((s) => `${s.label} ${s.total}`).join(', ')}`}
      className="overflow-visible"
    >
      {/* baseline + top gridline, kept faint per the design checklist */}
      {[0, niceMax].map((v) => (
        <g key={v}>
          <line
            x1={PAD.left}
            x2={W - PAD.right}
            y1={y(v)}
            y2={y(v)}
            stroke="var(--color-border-subtle)"
            strokeWidth={1}
          />
          <text
            x={PAD.left - 8}
            y={y(v) + 4}
            textAnchor="end"
            fill="var(--color-text-tertiary)"
            fontSize={11}
          >
            {v}
          </text>
        </g>
      ))}

      {Array.from({ length: groups }).map((_, g) => {
        const cx = PAD.left + groupW * g + groupW / 2
        return (
          <g key={g}>
            {series.map((s, i) => {
              const v = s.buckets[g]
              const h = Math.max(v > 0 ? 2 : 0, (v / niceMax) * plotH)
              return (
                <rect
                  key={s.channel}
                  x={cx - clusterW / 2 + i * barW}
                  y={PAD.top + plotH - h}
                  width={Math.max(1, barW - 1.5)}
                  height={h}
                  rx={2}
                  fill={CHANNEL_COLOR[s.channel]}
                />
              )
            })}
            <text
              x={cx}
              y={H - 8}
              textAnchor="middle"
              fill="var(--color-text-tertiary)"
              fontSize={11}
            >
              {DAY_LABELS[g] ?? g + 1}
            </text>
          </g>
        )
      })}
    </svg>
  )
}

export function ChannelLegend({ series }: { series: ChannelSeries[] }) {
  return (
    <ul className="mt-4 flex flex-wrap justify-between gap-x-4 gap-y-3">
      {series.map((s) => (
        <li key={s.channel} className="min-w-0">
          <span className="flex items-center gap-1.5">
            <span
              aria-hidden="true"
              className="h-2 w-2 flex-shrink-0 rounded-full"
              style={{ backgroundColor: CHANNEL_COLOR[s.channel] }}
            />
            <span className="truncate text-metadata text-text-secondary">{s.label}</span>
          </span>
          <span className="mt-1 block text-body-sm font-semibold text-text-primary">{s.total}</span>
        </li>
      ))}
    </ul>
  )
}
