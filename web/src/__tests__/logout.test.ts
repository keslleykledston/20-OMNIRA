import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('axios', () => ({
  default: {
    create: () => ({ post, interceptors: { request: { use: vi.fn() }, response: { use: vi.fn() } } }),
  },
}))

import { endSessionUrlFrom } from '../lib/logout'
import { authAPI } from '../lib/api'

describe('endSessionUrlFrom', () => {
  it('accepts an absolute https or http address', () => {
    expect(endSessionUrlFrom({ end_session_url: 'https://auth.example/logout?client_id=x' })).toBe('https://auth.example/logout?client_id=x')
    expect(endSessionUrlFrom({ end_session_url: 'http://localhost:8888/logout' })).toBe('http://localhost:8888/logout')
  })

  // The browser follows this address, so anything that is not a plain web URL is dropped.
  it('ignores anything that is not an absolute web URL', () => {
    for (const bad of ['javascript:alert(1)', 'data:text/html,hi', '/login', '//evil.example', 'not a url', '', 42, null, {}]) {
      expect(endSessionUrlFrom({ end_session_url: bad })).toBeUndefined()
    }
    expect(endSessionUrlFrom(undefined)).toBeUndefined()
    expect(endSessionUrlFrom(null)).toBeUndefined()
    expect(endSessionUrlFrom('')).toBeUndefined()
    expect(endSessionUrlFrom({})).toBeUndefined()
  })
})

describe('authAPI.logout', () => {
  beforeEach(() => {
    post.mockReset()
    localStorage.setItem('token', 't')
    localStorage.setItem('user', '{}')
  })

  it('returns the provider end-session address and clears the local session', async () => {
    post.mockResolvedValue({ status: 200, data: { end_session_url: 'https://auth.example/logout' } })
    const res = await authAPI.logout()
    expect(res.data.endSessionUrl).toBe('https://auth.example/logout')
    expect(localStorage.getItem('token')).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('returns no address on a 204 (nothing to end at the provider)', async () => {
    post.mockResolvedValue({ status: 204, data: '' })
    expect((await authAPI.logout()).data.endSessionUrl).toBeUndefined()
  })

  it('still clears the local session when the call fails', async () => {
    post.mockRejectedValue(new Error('network'))
    const res = await authAPI.logout()
    expect(res.data.endSessionUrl).toBeUndefined()
    expect(localStorage.getItem('token')).toBeNull()
  })
})
