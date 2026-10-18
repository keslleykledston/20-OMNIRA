import { describe, expect, it } from 'vitest'
import { directionOf, nextSort } from '../lib/sorting'

describe('nextSort', () => {
  it('walks the column cycle and returns to the default order after the last entry', () => {
    const cycle = ['name:asc', 'name:desc']
    expect(nextSort(undefined, cycle)).toBe('name:asc')
    expect(nextSort('name:asc', cycle)).toBe('name:desc')
    expect(nextSort('name:desc', cycle)).toBeUndefined()
  })
  it('starts the cycle when another column was sorted', () => {
    expect(nextSort('subject:asc', ['priority:desc', 'priority:asc'])).toBe('priority:desc')
  })
})

describe('directionOf', () => {
  it('says which way a column runs, none for the others, and the default direction of the default order', () => {
    expect(directionOf('name:desc', 'name')).toBe('desc')
    expect(directionOf('name:desc', 'subject')).toBe('none')
    expect(directionOf(undefined, 'name')).toBe('none')
    expect(directionOf(undefined, 'updated_at', 'desc')).toBe('desc')
    expect(directionOf('updated_at:asc', 'updated_at', 'desc')).toBe('asc')
    expect(directionOf('garbage', 'name')).toBe('none')
  })
})
