import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { PermissionState } from '../components/primitives'
import { PeopleAndGroups, type PeopleTab } from '../components/people/PeopleAndGroups'
import { agentsAPI } from '../lib/agents'
import { buildGroups, buildPeople } from '../lib/peopleModel'
import { queueErrorMessage, queuesAPI, type QueueMode } from '../lib/queues'
import { getTenantId } from '../lib/session'
import { teamAPI } from '../lib/team'
import { useAccess } from '../lib/useAccess'

const TAB_PARAM = 'aba'

// One page for the two things an administrator does to staff the attendance:
// choose who attends (an agent profile on a team member) and organise the groups
// (queues) they attend in. Authorization stays in the API: this page only avoids
// offering what the role matrix will refuse (agent.read / agent.manage).
export default function PeopleAndGroupsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()
  const tab: PeopleTab = params.get(TAB_PARAM) === 'grupos' ? 'groups' : 'people'
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const canRead = access.can('agent.read')
  const canManage = access.can('agent.manage')

  const team = useQuery({ queryKey: ['pg-team', tenantId], queryFn: teamAPI.list, enabled: canRead, retry: false })
  const agents = useQuery({ queryKey: ['pg-agents', tenantId], queryFn: agentsAPI.list, enabled: canRead, retry: false })
  const queues = useQuery({ queryKey: ['pg-queues', tenantId], queryFn: queuesAPI.list, enabled: canRead, retry: false })

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['pg-agents', tenantId] }),
      queryClient.invalidateQueries({ queryKey: ['pg-queues', tenantId] }),
      // The older Agentes page reads the same data under its own key.
      queryClient.invalidateQueries({ queryKey: ['agents', tenantId] }),
    ])

  // Runs one change: marks the item busy, clears the previous error, refreshes on
  // success and shows a sentence the operator can act on on failure.
  const run = async (key: string, action: () => Promise<unknown>) => {
    setBusyKey(key)
    setActionError(null)
    try {
      await action()
      await refresh()
    } catch (err) {
      setActionError(queueErrorMessage(err))
      // Even after a failure the screen may be stale (someone else changed it).
      void refresh()
    } finally {
      setBusyKey(null)
    }
  }

  if (!access.isLoading && !canRead) {
    return (
      <div className="px-6 py-6 lg:px-8 lg:py-8">
        <PermissionState message="Você não tem permissão para ver pessoas e grupos." />
      </div>
    )
  }

  const peopleReady = team.data && agents.data
  const people = team.isError || agents.isError
    ? { state: 'error' as const, items: [], error: 'Não foi possível carregar as pessoas.' }
    : peopleReady
      ? { state: 'ready' as const, items: buildPeople(team.data!, agents.data!) }
      : { state: 'loading' as const, items: [] }
  const groups = queues.isError
    ? { state: 'error' as const, items: [], error: 'Não foi possível carregar os grupos.' }
    : queues.data
      ? { state: 'ready' as const, items: buildGroups(queues.data) }
      : { state: 'loading' as const, items: [] }

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PeopleAndGroups
        tab={tab}
        onTabChange={(next) => {
          setActionError(null)
          setParams(next === 'groups' ? { [TAB_PARAM]: 'grupos' } : {}, { replace: true })
        }}
        people={people}
        groups={groups}
        canManage={canManage}
        busyKey={busyKey}
        actionError={actionError}
        onMakeOperator={(membershipId) => void run(membershipId, () => agentsAPI.create(membershipId))}
        onSetOperatorStatus={(profileId, status) => void run(profileId, () => agentsAPI.setStatus(profileId, status))}
        onAddToGroup={(profileId, groupId) => void run(`${profileId}:${groupId}`, () => agentsAPI.addQueue(profileId, groupId, true, 1))}
        onRemoveFromGroup={(profileId, memberId) => void run(memberId, () => agentsAPI.removeQueue(profileId, memberId))}
        onUpdateMembership={(profileId, memberId, v) => void run(memberId, () => agentsAPI.updateQueue(profileId, memberId, v.available, v.capacity))}
        onCreateGroup={(v) => void run('new-group', () => queuesAPI.create({ name: v.name, mode: v.mode, is_default: v.isDefault }))}
        onRenameGroup={(id, name) => void run(id, () => queuesAPI.update(id, { name }))}
        onChangeGroupMode={(id, mode: QueueMode) => void run(id, () => queuesAPI.update(id, { mode }))}
        onMakeDefault={(id) => void run(id, () => queuesAPI.update(id, { is_default: true }))}
        onDeleteGroup={(id) => void run(id, () => queuesAPI.remove(id))}
      />
    </div>
  )
}
