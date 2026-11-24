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

export const getRolloutProgress = (taskId: string) =>
  unwrap<{ progress: RolloutProgress }>(
    client.get(`/models/publish/tasks/${taskId}`),
  ).then((data) => data.progress);

export const retryFailedTenants = (taskId: string) =>
  unwrap<{ retried: number }>(
    client.post(`/models/publish/tasks/${taskId}/retry`),
  );
