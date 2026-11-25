import axios from 'axios';

import { Authorization } from '@/constants/authorization';
import { adminStorage } from '@/utils/authorization-util';

export type TenantStateCounts = {
  active: number;
  deprecated: number;
  tombstoned: number;
};

export type ManagedModel = {
  id: string;
  model_name: string;
  model_type: number;
  is_default: boolean;
  status: 'active' | 'retired';
  tenants: TenantStateCounts;
  active_kb_count: number;
  active_dialog_count: number;
  create_time: number;
};

export type RolloutResult = {
  task_id: string;
  total: number;
  affected_users_count?: number;
};

export type RolloutProgress = {
  task_id: string;
  model_name: string;
  status: 'running' | 'completed' | 'partial_failed';
  total: number;
  succeeded: number;
  pending: number;
  failed: number;
  upgraded_users_count: number;
  preserved_users_count: number;
};

export type RolloutFailedItem = {
  tenant_id: string;
  retry_count: number;
  last_error: string;
};

export type RolloutFailedPage = {
  total: number;
  page: number;
  page_size: number;
  items: RolloutFailedItem[];
};

export type RolloutTaskDetail = {
  progress: RolloutProgress;
  failed_items: RolloutFailedPage;
};

export type AdminHealthStatus = {
  status: 'ok' | 'degraded';
  service: string;
  gateway?: string;
  error?: string;
  latency_ms?: number;
};

export type DailyUsageItem = {
  tenant_id: string;
  model_name: string;
  model_type: number;
  usage_date: string;
  call_count: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cached_tokens: number;
  cache_write_tokens: number;
  reasoning_tokens: number;
  characters: number;
  total_spend: number;
  prompt_spend: number;
  completion_spend: number;
  cache_savings: number;
};

export type DailyUsagePage = {
  total: number;
  page: number;
  page_size: number;
  items: DailyUsageItem[];
};

export type ModelTypeUsageBreakdown = {
  model_type: number;
  call_count: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  characters: number;
  total_spend: number;
};

export type UsageSummary = {
  tenant_id: string;
  total_calls: number;
  total_prompt_tokens: number;
  total_completion_tokens: number;
  total_tokens: number;
  total_cached_tokens: number;
  total_cache_write_tokens: number;
  total_reasoning_tokens: number;
  total_characters: number;
  total_spend: number;
  total_prompt_spend: number;
  total_completion_spend: number;
  total_cache_savings: number;
  model_tokens: Record<string, number>;
  model_characters?: Record<string, number>;
  by_model_type?: Record<string, ModelTypeUsageBreakdown>;
  start_date?: string;
  end_date?: string;
};

export type UsageQueryParams = {
  tenant_id?: string;
  model_name?: string;
  model_type?: number;
  start_date?: string;
  end_date?: string;
  page?: number;
  page_size?: number;
};

export type AuditIsolationReport = {
  tenant_id: string;
  managed_spend_total: number;
  self_owned_spend_total: number;
  unmanaged_models_detected?: string[];
  audit_passed: boolean;
  violations?: string[];
};

export type TenantReconcileStatus =
  | 'ready'
  | 'pending'
  | 'provisioning'
  | 'degraded'
  | 'failed'
  | 'deleted';

export type TenantKeyItem = {
  tenant_id: string;
  status: TenantReconcileStatus;
  key_alias: string;
  key_fingerprint: string;
  key_rotated_at?: string;
  key_rotated_by?: string;
  key_dirty_since?: string;
};

export type TenantKeyListPage = {
  total: number;
  page_size: number;
  items: TenantKeyItem[];
};

export type TenantKeyQueryParams = {
  status?: TenantReconcileStatus;
  after?: string;
  page_size?: number;
};

export type KeyRotationResult = {
  tenant_id: string;
  key_alias: string;
  new_key_fingerprint: string;
  rotated_at: string;
  revoked_keys: number;
  old_keys_revoked: boolean;
};

type Envelope<T> = { code: number; data: T; message?: string };

const client = axios.create({
  baseURL: '/api/v1/admin/model-platform',
  timeout: 60000,
});

client.interceptors.request.use((config) => {
  config.headers.set(Authorization, adminStorage.getAuthorization());
  return config;
});

async function unwrap<T>(request: Promise<{ data: Envelope<T> }>): Promise<T> {
  try {
    const response = await request;
    return response.data.data;
  } catch (error) {
    if (axios.isAxiosError(error)) {
      throw new Error(error.response?.data?.message ?? error.message);
    }
    throw error;
  }
}

export const getAdminHealth = async (): Promise<AdminHealthStatus> => {
  const startedAt = performance.now();
  try {
    const response = await client.get<AdminHealthStatus>('/health');
    const latencyMs = Math.max(1, Math.round(performance.now() - startedAt));
    return { ...response.data, latency_ms: latencyMs };
  } catch (error) {
    if (axios.isAxiosError(error) && error.response?.data) {
      const latencyMs = Math.max(1, Math.round(performance.now() - startedAt));
      return {
        ...(error.response.data as AdminHealthStatus),
        latency_ms: latencyMs,
      };
    }
    throw error;
  }
};

export const listManagedModels = () =>
  unwrap<ManagedModel[]>(client.get('/models'));

export const onboardModel = (modelName: string, modelType: number) =>
  unwrap<RolloutResult>(
    client.post('/models', { model_name: modelName, model_type: modelType }),
  );

export const retireModel = (id: string) =>
  unwrap<RolloutResult>(client.post(`/models/${id}/retire`));

export const reactivateModel = (id: string) =>
  unwrap<RolloutResult>(client.post(`/models/${id}/reactivate`));

export const setDefaultModel = (id: string) =>
  unwrap<RolloutResult>(client.put(`/models/${id}/default`));

export const getRolloutTaskDetail = (taskId: string, page = 1, pageSize = 20) =>
  unwrap<RolloutTaskDetail>(
    client.get(`/models/publish/tasks/${taskId}`, {
      params: { page, page_size: pageSize },
    }),
  );

export const getRolloutProgress = (taskId: string) =>
  getRolloutTaskDetail(taskId, 1, 1).then((data) => data.progress);

export const retryFailedTenants = (taskId: string) =>
  unwrap<{ retried: number }>(
    client.post(`/models/publish/tasks/${taskId}/retry`),
  );

export const getDailyUsage = (params: UsageQueryParams) =>
  unwrap<DailyUsagePage>(client.get('/usage/daily', { params }));

export const getUsageSummary = (params: UsageQueryParams) =>
  unwrap<UsageSummary>(client.get('/usage/summary', { params }));

export const auditTenantUsage = (tenantId: string) =>
  unwrap<AuditIsolationReport>(
    client.get('/usage/audit', { params: { tenant_id: tenantId } }),
  );

export const listTenantKeys = (params: TenantKeyQueryParams = {}) =>
  unwrap<TenantKeyListPage>(client.get('/tenants', { params }));

export const rotateTenantKey = (tenantId: string) =>
  unwrap<KeyRotationResult>(
    client.post(`/tenants/${encodeURIComponent(tenantId)}/rotate-key`),
  );
