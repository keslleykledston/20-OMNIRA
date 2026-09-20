import type { DashboardData, PeriodId } from '../types'
import { fixtureDashboard } from './fixtures'

export interface DashboardRepository {
  getDashboard(period: PeriodId): Promise<DashboardData>
}

// The Go API has no dashboard endpoint yet. Swap this for an api-adapter that
// calls it; nothing in features/dashboard/components has to change.
export const fixtureRepository: DashboardRepository = {
  async getDashboard(period) {
    await new Promise((r) => setTimeout(r, 250))
    return fixtureDashboard(period)
  },
}

export const dashboardRepository: DashboardRepository = fixtureRepository
