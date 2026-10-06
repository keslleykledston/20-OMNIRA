import { create } from 'zustand'
import { clearSession } from './session'

export interface User {
  id: string
  email: string
  name: string
  roles: string[]
  password_expires_at?: string // ISO 8601 datetime, undefined se não há troca obrigatória
}

export interface AuthState {
  user: User | null
  token: string | null
  loading: boolean
  setUser: (user: User) => void
  setToken: (token: string) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  token: localStorage.getItem('token'),
  loading: false,
  setUser: (user) => set({ user }),
  setToken: (token) => {
    localStorage.setItem('token', token)
    set({ token })
  },
  logout: () => {
    clearSession()
    set({ user: null, token: null })
  }
}))

export interface UIState {
  sidebarOpen: boolean
  toggleSidebar: () => void
  setSidebarOpen: (open: boolean) => void
}

export const useUIStore = create<UIState>((set) => ({
  sidebarOpen: true,
  toggleSidebar: () => set(state => ({ sidebarOpen: !state.sidebarOpen })),
  setSidebarOpen: (open) => set({ sidebarOpen: open })
}))
