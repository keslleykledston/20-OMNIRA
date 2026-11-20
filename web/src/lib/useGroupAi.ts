import { useQuery } from '@tanstack/react-query'
import { getTenantId } from './session'
import { groupAiAPI, type ProposalStatus } from './groupAi'
import { analysisPending, consolidationPending, importInProgress } from './groupAiModel'

// Data hooks of the group copilot. Every key carries the tenant and the group, so a prefix invalidation
// (groupAiKey(groupId)) refreshes everything of one group and never anything of another.
export const groupAiKey = (groupId: string) => ['group-ai', getTenantId(), groupId] as const

const POLL_MS = 8_000

/**
 * The memory and where the reading stands. It is also the feature probe: 404 = the copilot is off (no panel),
 * 403 = this person may not read it (no panel). `poll` keeps refreshing while a "read now" is waiting for the worker.
 */
export function useGroupMemory(groupId: string, opts: { enabled: boolean; includeSuperseded?: boolean; poll?: boolean }) {
  const includeSuperseded = !!opts.includeSuperseded
  return useQuery({
    queryKey: [...groupAiKey(groupId), 'memory', includeSuperseded],
    queryFn: () => groupAiAPI.memory(groupId, includeSuperseded ? { include_superseded: true } : {}),
    enabled: opts.enabled && !!getTenantId() && !!groupId,
    retry: false,
    refetchInterval: (q) => (opts.poll && (analysisPending(q.state.data?.state) || consolidationPending(q.state.data?.state)) ? POLL_MS : false),
  })
}

export function useGroupImports(groupId: string, enabled: boolean) {
  return useQuery({
    queryKey: [...groupAiKey(groupId), 'imports'],
    queryFn: () => groupAiAPI.imports(groupId),
    enabled: enabled && !!getTenantId() && !!groupId,
    retry: false,
    // a file being read changes on its own: follow it until it settles
    refetchInterval: (q) => (q.state.data?.items.some(importInProgress) ? POLL_MS : false),
  })
}

export function useGroupProposals(groupId: string, enabled: boolean, status?: ProposalStatus) {
  return useQuery({
    queryKey: [...groupAiKey(groupId), 'proposals', status ?? 'all'],
    queryFn: () => groupAiAPI.proposals(groupId, status),
    enabled: enabled && !!getTenantId() && !!groupId,
    retry: false,
  })
}
