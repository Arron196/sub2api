/**
 * System API endpoints for admin operations
 */

import { apiClient } from '../client'

export interface ReleaseInfo {
  name: string
  body: string
  published_at: string
  html_url: string
}

export interface VersionInfo {
  current_version: string
  latest_version: string
  has_update: boolean
  release_info?: ReleaseInfo
  cached: boolean
  warning?: string
  build_type: string // "source" for manual builds, "release" for CI builds
}

/**
 * Get current version
 */
export async function getVersion(): Promise<{ version: string }> {
  const { data } = await apiClient.get<{ version: string }>('/admin/system/version')
  return data
}

/**
 * Check for updates
 * @param force - Force refresh from GitHub API
 */
export async function checkUpdates(force = false): Promise<VersionInfo> {
  const { data } = await apiClient.get<VersionInfo>('/admin/system/check-updates', {
    params: force ? { force: 'true' } : undefined
  })
  return data
}

export interface UpdateResult {
  message: string
  need_restart: boolean
}

export interface SystemDebugSectionStatus {
  status: string
  error_kind?: string
}

export interface SystemDebugServerConditions {
  status: string
  collected_at: string
  cpu: SystemDebugSectionStatus & {
    logical_count?: number
    percent?: number
    load1?: number
    load5?: number
    load15?: number
  }
  memory: SystemDebugSectionStatus & {
    total_bytes?: number
    available_bytes?: number
    used_bytes?: number
    used_percent?: number
  }
  disk: SystemDebugSectionStatus & {
    volumes: Array<{
      label: string
      fstype?: string
      total_bytes: number
      free_bytes: number
      used_bytes: number
      used_percent: number
      inodes_used_percent?: number
    }>
  }
  host: SystemDebugSectionStatus & {
    uptime_seconds?: number
  }
  process: SystemDebugSectionStatus & {
    rss_bytes?: number
    vms_bytes?: number
    cpu_percent?: number
    num_threads?: number
    num_fds?: number
  }
  database: SystemDebugSectionStatus & {
    latency_ms?: number
    stats: {
      open_connections: number
      in_use: number
      idle: number
      wait_count: number
      wait_duration_ms: number
      max_open: number
    }
  }
  redis: SystemDebugSectionStatus & {
    latency_ms?: number
    pool: {
      total: number
      idle: number
      stale: number
      hits: number
      misses: number
      timeouts: number
    }
  }
  latest_ops_metrics: SystemDebugSectionStatus & {
    snapshot?: {
      created_at: string
      window_minutes: number
      cpu_usage_percent?: number
      memory_used_mb?: number
      memory_total_mb?: number
      memory_usage_percent?: number
      db_ok?: boolean
      redis_ok?: boolean
      db_max_open_conns?: number
      redis_pool_size?: number
      redis_conn_total?: number
      redis_conn_idle?: number
      db_conn_active?: number
      db_conn_idle?: number
      db_conn_waiting?: number
      goroutine_count?: number
      concurrency_queue_depth?: number
      account_switch_count?: number
    }
  }
  ops_job_heartbeats: SystemDebugSectionStatus & {
    limit: number
    count: number
    truncated: boolean
    heartbeats: Array<{
      job_name: string
      last_run_at?: string
      last_success_at?: string
      last_error_at?: string
      last_duration_ms?: number
      updated_at: string
      last_error?: string
    }>
  }
}

export interface SystemDebugLogAttribution {
  status: string
  error_kind?: string
  window: {
    preset: SystemDebugLogWindowPreset | 'detail_default'
    start: string
    end: string
    window_seconds: number
    window_minutes: number
    max_window_seconds: number
  }
  window_hours: number
  limit: number
  capabilities: string[]
  limitations: string[]
  system_logs: SystemDebugSectionStatus & {
    total_count: number
    total_count_exact: boolean
    sample_count: number
    truncated: boolean
    by_level: Array<{ value: string; count: number }>
    by_component: Array<{ value: string; count: number }>
    samples: Array<{
      created_at: string
      level: string
      component: string
      request_id?: string
      client_request_id?: string
      platform?: string
      model?: string
      message_excerpt?: string
    }>
  }
  error_logs: SystemDebugSectionStatus & {
    total_count: number
    total_count_exact: boolean
    sample_count: number
    truncated: boolean
    by_phase: Array<{ value: string; count: number }>
    by_type: Array<{ value: string; count: number }>
    by_owner: Array<{ value: string; count: number }>
    by_source: Array<{ value: string; count: number }>
    by_status: Array<{ value: string; count: number }>
    samples: Array<{
      created_at: string
      request_id?: string
      client_request_id?: string
      phase?: string
      type?: string
      owner?: string
      source?: string
      severity?: string
      status_code?: number
      platform?: string
      model?: string
      request_path?: string
      inbound_endpoint?: string
      upstream_endpoint?: string
      message_excerpt?: string
    }>
  }
  diagnostic_hints: Array<{
    kind: string
    summary: string
  }>
}

export type SystemDebugExportDetailLevel = 'standard' | 'detailed' | 'support'
export type SystemDebugSensitiveHandling = 'masked' | 'diagnostic'
export type SystemDebugLogWindowPreset = '30m' | '6h' | '1d' | '3d' | '1w' | 'custom'

export interface SystemDebugExportOptions {
  detail_level: SystemDebugExportDetailLevel
  sensitive_handling: SystemDebugSensitiveHandling
  log_window_preset?: SystemDebugLogWindowPreset
  custom_log_start?: string
  custom_log_end?: string
}

export type DebugExportJobStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'expired'

export interface DebugExportJob {
  id: number
  status: DebugExportJobStatus
  options: SystemDebugExportOptions
  created_by: number
  progress_percent: number
  phase: string
  bytes_written: number
  file_name?: string
  file_size?: number
  sha256?: string
  error_message?: string
  canceled_by?: number
  canceled_at?: string
  started_at?: string
  finished_at?: string
  expires_at?: string
  last_heartbeat_at?: string
  created_at: string
  updated_at: string
}

export interface DebugExportJobListResponse {
  items: DebugExportJob[]
}

export interface SystemDebugExportSensitiveDiagnostics {
  status: string
  handling: SystemDebugSensitiveHandling
  notices: string[]
  items: Array<{
    item_name: string
    value_category: string
    configured: boolean
    length_bucket?: string
    fingerprint?: string
    format_hint?: string
    notes?: string[]
  }>
}

export interface SystemDebugExportBundle {
  schema_version: string
  generated_at: string
  manifest: {
    detail_level: SystemDebugExportDetailLevel
    sensitive_handling: SystemDebugSensitiveHandling
    generated_for: string
    included_sections: string[]
    safety_notes: string[]
    limits: {
      account_scheduling_samples: number
      log_attribution_samples: number
      job_heartbeat_samples: number
      log_attribution_window_hours: number
      log_attribution_window_minutes: number
      log_attribution_window_seconds: number
      max_log_attribution_window_seconds: number
    }
    timeouts: {
      export_seconds: number
      probe_milliseconds: number
      account_scheduling_seconds: number
    }
  }
  redaction: {
    mode: string
    sensitive_handling: SystemDebugSensitiveHandling
    marker: string
    final_pass: string
    excluded_sections: string[]
  }
  system: {
    version: string
    build_type: string
    run_mode: string
    timezone: string
  }
  runtime: Record<string, string | number>
  server_conditions: SystemDebugServerConditions
  configuration: Record<string, Record<string, string | number | boolean>>
  ops: {
    error_log_queue: {
      length: number
      capacity: number
      dropped_total: number
      enqueued_total: number
      processed_total: number
      sanitized_total: number
    }
  }
  log_attribution: SystemDebugLogAttribution
  sensitive_diagnostics: SystemDebugExportSensitiveDiagnostics
  account_scheduling: {
    sample_limit: number
    matching_count: number
    sample_count: number
    truncated: boolean
    collection_error?: string
    summary: {
      by_platform: Array<{ value: string; count: number }>
      by_type: Array<{ value: string; count: number }>
      by_status: Array<{ value: string; count: number }>
    }
    blocker_counts: Record<string, number>
    samples: Array<{
      account_id: number
      platform: string
      type: string
      status: string
      schedulable: boolean
      last_used_at?: string
      expires_at?: string
      rate_limited_at?: string
      rate_limit_reset_at?: string
      overload_until?: string
      temp_unschedulable_until?: string
      session_window_start?: string
      session_window_end?: string
      session_window_status?: string
      blockers: string[]
      error_message?: string
      temp_unschedulable_reason?: string
    }>
  }
}

/**
 * Perform system update
 * Downloads and applies the latest version
 */
export async function performUpdate(): Promise<UpdateResult> {
  const { data } = await apiClient.post<UpdateResult>('/admin/system/update')
  return data
}

/**
 * Rollback to previous version
 */
export async function rollback(): Promise<UpdateResult> {
  const { data } = await apiClient.post<UpdateResult>('/admin/system/rollback')
  return data
}

/**
 * Restart the service
 */
export async function restartService(): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>('/admin/system/restart')
  return data
}

/**
 * Export a redacted system debug bundle.
 */
export async function exportDebugData(
  options: SystemDebugExportOptions = {
    detail_level: 'standard',
    sensitive_handling: 'masked',
    log_window_preset: '1d'
  }
): Promise<SystemDebugExportBundle> {
  const { data } = await apiClient.post<SystemDebugExportBundle>('/admin/system/debug-export', options)
  return data
}

export async function createDebugExportJob(options: SystemDebugExportOptions): Promise<DebugExportJob> {
  const { data } = await apiClient.post<DebugExportJob>('/admin/system/debug-export-jobs', options)
  return data
}

export async function listDebugExportJobs(): Promise<DebugExportJobListResponse> {
  const { data } = await apiClient.get<DebugExportJobListResponse>('/admin/system/debug-export-jobs')
  return data
}

export async function getDebugExportJob(id: number): Promise<DebugExportJob> {
  const { data } = await apiClient.get<DebugExportJob>(`/admin/system/debug-export-jobs/${id}`)
  return data
}

export async function cancelDebugExportJob(id: number): Promise<DebugExportJob> {
  const { data } = await apiClient.post<DebugExportJob>(`/admin/system/debug-export-jobs/${id}/cancel`)
  return data
}

export async function downloadDebugExportJobArtifact(id: number): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`/admin/system/debug-export-jobs/${id}/download`, {
    responseType: 'blob'
  })
  return data
}

export const systemAPI = {
  getVersion,
  checkUpdates,
  performUpdate,
  rollback,
  restartService,
  exportDebugData,
  createDebugExportJob,
  listDebugExportJobs,
  getDebugExportJob,
  cancelDebugExportJob,
  downloadDebugExportJobArtifact
}

export default systemAPI
