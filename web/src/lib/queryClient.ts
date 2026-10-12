import { QueryClient } from '@tanstack/react-query'

// The ONE query cache of the app. It lives in a module (not inside App) so that signing out or signing in can forget what the previous person loaded: the keys of
// the queries do not carry the person, so nothing may survive a change of session.
export const queryClient = new QueryClient()
