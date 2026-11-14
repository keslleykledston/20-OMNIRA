import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// /platform/group-sla-alert (ADR-0058 addendum): the operator kill switch for the group SLA alert
// channel, replacing OMNIRA_GROUP_SLA_ALERT_ENABLED + a worker restart with a button here. Same
// authority model as the rest of platformRelease.ts — IsPlatformOperator, decided by the server from
// the session, never sent by this module.
export interface GroupSlaAlertFeatureFlag {
  enabled: boolean
  updated_by?: string
  updated_at: string
}

export const groupSlaAlertFeatureFlagAPI = {
  get: (): Promise<GroupSlaAlertFeatureFlag> =>
    axios.get<GroupSlaAlertFeatureFlag>(`${API_BASE}/platform/group-sla-alert`, { headers: authHeaders() }).then((r) => r.data),
  set: (enabled: boolean): Promise<GroupSlaAlertFeatureFlag> =>
    axios.put<GroupSlaAlertFeatureFlag>(`${API_BASE}/platform/group-sla-alert`, { enabled }, { headers: authHeaders() }).then((r) => r.data),
}

// /platform/context-intelligence-v2: the operator kill switch for the CONTEXT.2 window analysis
// runner — a SECOND gate, layered on top of (never replacing) the host-level
// OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED env var, which decides whether the subsystem is wired up at
// all. This switch only has any effect on a host where that env var is already true.
export interface ContextIntelligenceV2FeatureFlag {
  enabled: boolean
  updated_by?: string
  updated_at: string
}

export const contextIntelligenceV2FeatureFlagAPI = {
  get: (): Promise<ContextIntelligenceV2FeatureFlag> =>
    axios.get<ContextIntelligenceV2FeatureFlag>(`${API_BASE}/platform/context-intelligence-v2`, { headers: authHeaders() }).then((r) => r.data),
  set: (enabled: boolean): Promise<ContextIntelligenceV2FeatureFlag> =>
    axios.put<ContextIntelligenceV2FeatureFlag>(`${API_BASE}/platform/context-intelligence-v2`, { enabled }, { headers: authHeaders() }).then((r) => r.data),
}
