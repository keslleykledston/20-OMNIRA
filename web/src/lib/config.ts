// Same-origin by default: the dev server (vite proxy) and nginx forward /api to the backend.
// Override with VITE_API_BASE only for a deliberate cross-origin setup (needs CORS on the API).
export const API_BASE: string = (import.meta.env?.VITE_API_BASE as string | undefined) ?? '/api/v1';
