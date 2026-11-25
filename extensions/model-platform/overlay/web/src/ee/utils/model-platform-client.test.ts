import axios, {
  AxiosError,
  AxiosHeaders,
  type AxiosInstance,
  type InternalAxiosRequestConfig,
} from 'axios';

import { AdminAuthorization, Authorization } from '@/constants/authorization';

let capturedClient: AxiosInstance | undefined;
const realCreate = axios.create.bind(axios);
const createSpy = jest.spyOn(axios, 'create').mockImplementation((config) => {
  const instance = realCreate(config);
  capturedClient = instance;
  return instance;
});

// Require after spying on axios.create so we capture the module-scoped instance
// and can attach a custom adapter without mocking axios internals.
// eslint-disable-next-line @typescript-eslint/no-require-imports
const api =
  require('./model-platform-client') as typeof import('./model-platform-client');

createSpy.mockRestore();

function getClient(): AxiosInstance {
  if (!capturedClient) {
    throw new Error(
      'Expected model-platform-client to create an Axios instance',
    );
  }
  return capturedClient;
}

describe('model-platform-client', () => {
  const originalAdapter = getClient().defaults.adapter;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    getClient().defaults.adapter = originalAdapter;
    jest.restoreAllMocks();
  });

  it('attaches the admin Authorization header and unwraps data envelopes', async () => {
    localStorage.setItem(AdminAuthorization, 'Bearer admin-token-123');
    const capturedRequests: InternalAxiosRequestConfig[] = [];

    getClient().defaults.adapter = async (config) => {
      capturedRequests.push(config);
      return {
        data: {
          code: 0,
          data: [
            {
              id: 'm1',
              model_name: 'gpt-4o',
              model_type: 1,
              is_default: true,
              status: 'active',
              tenants: { active: 3, deprecated: 0, tombstoned: 0 },
              active_kb_count: 0,
              active_dialog_count: 2,
              create_time: 1700000000,
            },
          ],
        },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      };
    };

    const models = await api.listManagedModels();

    expect(models).toHaveLength(1);
    expect(models[0].model_name).toBe('gpt-4o');
    expect(capturedRequests).toHaveLength(1);
    expect(capturedRequests[0].baseURL).toBe('/api/v1/admin/model-platform');
    expect(capturedRequests[0].url).toBe('/models');
    expect(capturedRequests[0].method).toBe('get');
    expect(capturedRequests[0].headers.get(Authorization)).toBe(
      'Bearer admin-token-123',
    );
  });

  it('prefers response.data.message over Axios error.message on HTTP failure', async () => {
    getClient().defaults.adapter = async (config) => {
      throw new AxiosError(
        'Request failed with status code 409',
        'ERR_BAD_REQUEST',
        config,
        {},
        {
          data: { code: 409, message: 'model already exists in catalog' },
          status: 409,
          statusText: 'Conflict',
          headers: new AxiosHeaders(),
          config,
        },
      );
    };

    await expect(api.onboardModel('gpt-4o', 1)).rejects.toThrow(
      'model already exists in catalog',
    );
  });

  it('falls back to Axios error.message when response body has no message', async () => {
    getClient().defaults.adapter = async (config) => {
      throw new AxiosError('Network Error', 'ERR_NETWORK', config);
    };

    await expect(api.listManagedModels()).rejects.toThrow('Network Error');
  });

  it('rethrows non-Axios errors unchanged', async () => {
    const customError = new TypeError('unexpected adapter failure');
    getClient().defaults.adapter = async () => {
      throw customError;
    };

    await expect(api.listManagedModels()).rejects.toBe(customError);
  });

  describe('getAdminHealth', () => {
    it('returns health payload with positive latency_ms on HTTP 200', async () => {
      getClient().defaults.adapter = async (config) => ({
        data: { status: 'ok', service: 'model-platform', gateway: 'healthy' },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      });

      const health = await api.getAdminHealth();

      expect(health.status).toBe('ok');
      expect(health.gateway).toBe('healthy');
      expect(health.latency_ms).toBeGreaterThanOrEqual(1);
    });

    it('returns degraded health payload on HTTP 503 with response data', async () => {
      getClient().defaults.adapter = async (config) => {
        throw new AxiosError(
          'Service Unavailable',
          'ERR_BAD_RESPONSE',
          config,
          {},
          {
            data: {
              status: 'degraded',
              service: 'model-platform',
              gateway: 'unreachable',
              error: 'dial tcp 127.0.0.1:4000: connection refused',
            },
            status: 503,
            statusText: 'Service Unavailable',
            headers: new AxiosHeaders(),
            config,
          },
        );
      };

      const health = await api.getAdminHealth();

      expect(health.status).toBe('degraded');
      expect(health.error).toBe('dial tcp 127.0.0.1:4000: connection refused');
      expect(health.latency_ms).toBeGreaterThanOrEqual(1);
    });

    it('rethrows when gateway health check fails with no response body', async () => {
      getClient().defaults.adapter = async (config) => {
        throw new AxiosError('ECONNRESET', 'ERR_NETWORK', config);
      };

      await expect(api.getAdminHealth()).rejects.toThrow('ECONNRESET');
    });
  });

  describe('endpoint contracts', () => {
    it('sends expected method, URL, payload, and query params for catalog, rollout, usage, and tenant APIs', async () => {
      const seen: Array<{
        method?: string;
        url?: string;
        data?: unknown;
        params?: unknown;
      }> = [];

      getClient().defaults.adapter = async (config) => {
        seen.push({
          method: config.method,
          url: config.url,
          data:
            typeof config.data === 'string'
              ? JSON.parse(config.data)
              : config.data,
          params: config.params,
        });

        const payload =
          config.url?.startsWith('/models/publish/tasks/') &&
          !config.url.endsWith('/retry')
            ? {
                progress: {
                  task_id: 'task-9',
                  model_name: 'gpt-4o',
                  status: 'completed',
                  total: 10,
                  succeeded: 10,
                  pending: 0,
                  failed: 0,
                  upgraded_users_count: 5,
                  preserved_users_count: 5,
                },
                failed_items: { total: 0, page: 1, page_size: 1, items: [] },
              }
            : { ok: true };

        return {
          data: { code: 0, data: payload },
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        };
      };

      await api.onboardModel('tts-1', 32);
      await api.retireModel('model-1');
      await api.reactivateModel('model-1');
      await api.setDefaultModel('model-1');
      await api.getRolloutTaskDetail('task-9', 2, 10);
      const progress = await api.getRolloutProgress('task-9');
      await api.retryFailedTenants('task-9');
      await api.getDailyUsage({
        tenant_id: 't1',
        model_type: 32,
        page: 2,
        page_size: 20,
      });
      await api.getUsageSummary({ start_date: '2026-11-01' });
      await api.auditTenantUsage('tenant-a');
      await api.listTenantKeys({ status: 'degraded', after: 't-10' });
      await api.rotateTenantKey('tenant/with space?x=1');

      expect(progress.task_id).toBe('task-9');
      expect(seen).toEqual([
        {
          method: 'post',
          url: '/models',
          data: { model_name: 'tts-1', model_type: 32 },
          params: undefined,
        },
        {
          method: 'post',
          url: '/models/model-1/retire',
          data: undefined,
          params: undefined,
        },
        {
          method: 'post',
          url: '/models/model-1/reactivate',
          data: undefined,
          params: undefined,
        },
        {
          method: 'put',
          url: '/models/model-1/default',
          data: undefined,
          params: undefined,
        },
        {
          method: 'get',
          url: '/models/publish/tasks/task-9',
          data: undefined,
          params: { page: 2, page_size: 10 },
        },
        {
          method: 'get',
          url: '/models/publish/tasks/task-9',
          data: undefined,
          params: { page: 1, page_size: 1 },
        },
        {
          method: 'post',
          url: '/models/publish/tasks/task-9/retry',
          data: undefined,
          params: undefined,
        },
        {
          method: 'get',
          url: '/usage/daily',
          data: undefined,
          params: { tenant_id: 't1', model_type: 32, page: 2, page_size: 20 },
        },
        {
          method: 'get',
          url: '/usage/summary',
          data: undefined,
          params: { start_date: '2026-11-01' },
        },
        {
          method: 'get',
          url: '/usage/audit',
          data: undefined,
          params: { tenant_id: 'tenant-a' },
        },
        {
          method: 'get',
          url: '/tenants',
          data: undefined,
          params: { status: 'degraded', after: 't-10' },
        },
        {
          method: 'post',
          url: '/tenants/tenant%2Fwith%20space%3Fx%3D1/rotate-key',
          data: undefined,
          params: undefined,
        },
      ]);
    });
  });
});
