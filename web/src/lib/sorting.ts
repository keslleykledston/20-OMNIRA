// Sorting a list by its column headers. The server owns the orderings (`sort=<field>:<asc|desc>`); a page only walks a short cycle per column.

/** The order a click moves to: the next entry of the column's cycle after the current sort, and back to the default (undefined) after the last. */
export function nextSort(current: string | undefined, cycle: string[]): string | undefined {
  const i = current === undefined ? -1 : cycle.indexOf(current)
  if (i === -1) return cycle[0]
  return cycle[i + 1] // undefined after the last: the default order again
}

/** Which way a column is ordered now. `defaultDirection` is how the default order (no sort sent) runs on that column, if it is that column's at all. */
export function directionOf(current: string | undefined, field: string, defaultDirection?: 'asc' | 'desc'): 'asc' | 'desc' | 'none' {
  if (current === undefined) return defaultDirection ?? 'none'
  const [f, dir] = current.split(':')
  return f === field && (dir === 'asc' || dir === 'desc') ? dir : 'none'
}
