import clsx from 'clsx'

// Single outline icon family for the Omnira shell.
// Inline SVG (the pattern already used in this repo) so no icon dependency is added.
export type IconName =
  | 'dashboard'
  | 'conversations'
  | 'tickets'
  | 'contacts'
  | 'channels'
  | 'reports'
  | 'supervisor'
  | 'search'
  | 'close'
  | 'more'
  | 'clock'
  | 'calendar'
  | 'chevron-down'
  | 'arrow-up'
  | 'arrow-down'
  | 'arrow-left'
  | 'check'
  | 'copy'
  | 'info'
  | 'plus'
  | 'whatsapp'

const paths: Record<IconName, string> = {
  dashboard: 'M4 13h6V4H4v9Zm0 7h6v-5H4v5Zm10 0h6v-9h-6v9Zm0-16v5h6V4h-6Z',
  conversations: 'M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.4A8 8 0 1 1 21 12Z',
  tickets: 'M4 8V6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v2a2 2 0 0 0 0 4v2a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-2a2 2 0 0 0 0-4Z',
  contacts: 'M16 19v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1M12 7a3 3 0 1 1-6 0 3 3 0 0 1 6 0Zm9 12v-1a4 4 0 0 0-3-3.9M16 4.1a4 4 0 0 1 0 7.8',
  channels: 'M7 8h10M7 12h6M21 12a9 9 0 0 1-13.1 8L3 21l1-4.9A9 9 0 1 1 21 12Z',
  reports: 'M4 20V10m5 10V4m5 16v-7m5 7V7',
  supervisor: 'M12 3 4 7v5c0 4.4 3.4 8.4 8 9 4.6-.6 8-4.6 8-9V7l-8-4Zm0 6v4m0 3h.01',
  search: 'M21 21l-4.3-4.3M17 11a6 6 0 1 1-12 0 6 6 0 0 1 12 0Z',
  close: 'M6 6l12 12M18 6 6 18',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  clock: 'M12 7v5l3 2m6-2a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z',
  calendar: 'M8 3v4m8-4v4M4 9h16M5 5h14a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Z',
  'chevron-down': 'm6 9 6 6 6-6',
  'arrow-up': 'M12 19V5m0 0-6 6m6-6 6 6',
  'arrow-down': 'M12 5v14m0 0 6-6m-6 6-6-6',
  'arrow-left': 'M19 12H5m0 0 6-6m-6 6 6 6',
  check: 'm5 13 4 4L19 7',
  copy: 'M8 8V5a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-3M5 8h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z',
  info: 'M12 16v-4m0-4h.01M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z',
  plus: 'M12 5v14M5 12h14',
  whatsapp: 'M21 11.5a8.5 8.5 0 0 1-12.3 7.6L4 20.5l1.5-4.6A8.5 8.5 0 1 1 21 11.5Zm-12 -2.2c0 3.2 2.6 5.8 5.8 5.8.6 0 1-.4 1-.9v-.9c0-.4-.3-.7-.7-.7-.3 0-.6.1-.9.2l-1.3-1.3c.1-.3.2-.6.2-.9 0-.4-.3-.7-.7-.7h-.9c-.5 0-.9.4-.9 1Z',
}

interface IconProps {
  name: IconName
  size?: number
  className?: string
}

export function Icon({ name, size = 20, className }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      className={clsx('flex-shrink-0', className)}
    >
      <path d={paths[name]} />
    </svg>
  )
}
