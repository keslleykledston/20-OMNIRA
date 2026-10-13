import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { BrandMark } from '../components/BrandMark'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

describe('BrandMark', () => {
  it('é decorativa sem rótulo e descrita com rótulo', () => {
    const { container, rerender } = render(<BrandMark />)
    expect(container.querySelector('img')?.getAttribute('aria-hidden')).toBe('true')
    rerender(<BrandMark label="OMNIRA" />)
    expect(screen.getByRole('img', { name: 'OMNIRA' }).getAttribute('src')).toBe('/omnira.svg')
  })

  it('os arquivos de marca referenciados pelo index.html existem em public/', () => {
    const root = resolve(__dirname, '../..')
    const html = readFileSync(resolve(root, 'index.html'), 'utf8')
    const hrefs = [...html.matchAll(/href="(\/[^"]+)"/g)].map((m) => m[1])
    expect(hrefs.length).toBeGreaterThanOrEqual(4)
    for (const h of hrefs) expect(existsSync(resolve(root, 'public', h.slice(1))), h).toBe(true)
    const manifest = JSON.parse(readFileSync(resolve(root, 'public/manifest.webmanifest'), 'utf8'))
    for (const i of manifest.icons) expect(existsSync(resolve(root, 'public', i.src.slice(1))), i.src).toBe(true)
  })
})
