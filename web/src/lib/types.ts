export interface Account {
  id: string
  tenant_id: string
  name: string
  account_type: 'operator' | 'contact_center' | 'reseller'
  status: 'active' | 'inactive' | 'suspended'
  sla_configuration?: SLAConfig
  created_at: string
  updated_at: string
}

export interface SLAConfig {
  first_response_target_hours: number
  resolution_target_hours: number
}

export interface Report {
  id: string
  template_id: string
  status: 'pending' | 'completed' | 'failed'
  data?: any
  duration_ms?: number
  created_at: string
  completed_at?: string
}

export interface ReportTemplate {
  id: string
  name: string
  columns: ReportColumn[]
  filters?: ReportFilter[]
  sorting?: SortConfig[]
  created_at: string
}

export interface ReportColumn {
  field: string
  label: string
  type: 'string' | 'number' | 'date' | 'boolean'
  sortable: boolean
  filterable: boolean
}

export interface ReportFilter {
  field: string
  operator: 'eq' | 'ne' | 'gt' | 'lt' | 'contains' | 'in' | 'between'
  value: any
}

export interface SortConfig {
  field: string
  direction: 'asc' | 'desc'
}

export interface Pagination {
  cursor?: string
  limit: number
}

export interface PaginatedResponse<T> {
  data: T[]
  pagination: {
    next_cursor?: string
    has_more: boolean
  }
}

export interface APIError {
  code: string
  message: string
  details?: Record<string, any>
}
