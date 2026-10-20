// ESLint flat config (ESLint 9) for the OMNIRA frontend (React 18 + TypeScript 5 + Vite 5).
// Added in F2 of the release-engineering program: `npm run lint` existed before (package.json)
// but had no config at all and failed immediately on every run — the CI "lint" gate the
// specification asks for (F2 §4.2) cannot be real without this.
//
// Scope kept close to the Vite React-TS template defaults (recommended JS + TS rules,
// react-hooks, react-refresh for Vite's fast-refresh correctness) — no project-specific rule
// tuning in this pass; that is a separate decision for whoever owns the frontend's style.
import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'coverage', 'playwright-report', 'test-results'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      // Many existing modules intentionally type external payloads loosely
      // (API responses, provider SDKs) — a blanket ban here would force a
      // drive-by refactor of unrelated code just to turn the lint gate on,
      // exactly what this program's own rules (CLAUDE.md) say not to do.
      // Tightened per-module by whoever owns that code, not globally here.
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
    },
  },
)
