import { describe, expect, it } from 'vitest'
import { windowLeft } from '../lib/windowLeft'

const H = 3_600_000
describe('windowLeft', () => {
  const now = Date.UTC(2026, 9, 17, 12, 0, 0)
  it('says hours and minutes, and is calm above two hours', () => {
    expect(windowLeft(new Date(now + 5 * H + 12 * 60_000), now)).toEqual({ text: 'restam 5 h 12 min', urgent: false, closed: false })
    expect(windowLeft(new Date(now + 3 * H), now).text).toBe('restam 3 h')
  })
  it('turns urgent under two hours and counts minutes', () => {
    expect(windowLeft(new Date(now + 2 * H - 60_000), now)).toMatchObject({ text: 'restam 1 h 59 min', urgent: true })
    expect(windowLeft(new Date(now + 42 * 60_000), now)).toMatchObject({ text: 'restam 42 min', urgent: true })
    expect(windowLeft(new Date(now + 30_000), now).text).toBe('resta menos de 1 min')
  })
  it('is closed at and after the deadline, and for garbage', () => {
    expect(windowLeft(new Date(now), now)).toMatchObject({ closed: true, urgent: true })
    expect(windowLeft('not a date', now).closed).toBe(true)
  })
})
