import type { TicketStatusSlice } from '../types'
import { TICKET_STATUS_COLOR } from './palette'

const SIZE = 180
const STROKE = 26
const R = (SIZE - STROKE) / 2
const C = 2 * Math.PI * R

export function TicketStatusDonut({ slices }: { slices: TicketStatusSlice[] }) {
  const total = slices.reduce((sum, s) => sum + s.count, 0)

  let offset = 0
  const arcs = slices.map((s) => {
    const fraction = total > 0 ? s.count / total : 0
    const arc = { slice: s, dash: fraction * C, offset }
    offset += fraction * C
    return arc
  })

  return (
    <div className="relative mx-auto" style={{ width: SIZE, height: SIZE }}>
      <svg
        viewBox={`0 0 ${SIZE} ${SIZE}`}
        width="100%"
        height="100%"
        role="img"
        aria-label={`Status dos tickets, ${total} no total: ${slices.map((s) => `${s.label} ${s.count}`).join(', ')}`}
      >
        <g transform={`rotate(-90 ${SIZE / 2} ${SIZE / 2})`}>
          <circle
            cx={SIZE / 2}
            cy={SIZE / 2}
            r={R}
            fill="none"
            stroke="var(--color-surface-muted)"
            strokeWidth={STROKE}
          />
          {arcs.map(({ slice, dash, offset: o }) => (
            <circle
              key={slice.status}
              cx={SIZE / 2}
              cy={SIZE / 2}
              r={R}
              fill="none"
              stroke={TICKET_STATUS_COLOR[slice.status]}
              strokeWidth={STROKE}
              strokeDasharray={`${dash} ${C - dash}`}
              strokeDashoffset={-o}
            />
          ))}
        </g>
      </svg>
      <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-display-md font-bold text-text-primary">{total}</span>
        <span className="text-metadata">Total</span>
      </div>
    </div>
  )
}

export function TicketStatusLegend({ slices }: { slices: TicketStatusSlice[] }) {
  return (
    <ul className="flex-1 space-y-3">
      {slices.map((s) => (
        <li key={s.status} className="flex items-center gap-2">
          <span
            aria-hidden="true"
            className="h-2 w-2 flex-shrink-0 rounded-full"
            style={{ backgroundColor: TICKET_STATUS_COLOR[s.status] }}
          />
          <span className="flex-1 truncate text-body-sm text-text-secondary">{s.label}</span>
          <span className="text-body-sm font-semibold text-text-primary">{s.count}</span>
        </li>
      ))}
    </ul>
  )
}
