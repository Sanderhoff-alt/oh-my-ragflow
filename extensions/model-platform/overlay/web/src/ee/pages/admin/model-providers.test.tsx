import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from '@testing-library/react';

import {
  formatTestTranslation as mockFormatTranslation,
  renderWithQueryClient,
} from '@/ee/__test-utils__/query-test-utils';
import ModelProvidersPage from './model-providers';

const mockListManagedModels = jest.fn();
const mockGetAdminHealth = jest.fn();
const mockOnboardModel = jest.fn();
const mockRetireModel = jest.fn();
const mockReactivateModel = jest.fn();
const mockSetDefaultModel = jest.fn();
const mockGetRolloutTaskDetail = jest.fn();
const mockRetryFailedTenants = jest.fn();
const mockMessageError = jest.fn();

jest.mock('@/ee/utils/model-platform-client', () => ({
  listManagedModels: (...args: unknown[]) => mockListManagedModels(...args),
  getAdminHealth: (...args: unknown[]) => mockGetAdminHealth(...args),
  onboardModel: (...args: unknown[]) => mockOnboardModel(...args),
  retireModel: (...args: unknown[]) => mockRetireModel(...args),
  reactivateModel: (...args: unknown[]) => mockReactivateModel(...args),
  setDefaultModel: (...args: unknown[]) => mockSetDefaultModel(...args),
  getRolloutTaskDetail: (...args: unknown[]) =>
    mockGetRolloutTaskDetail(...args),
  retryFailedTenants: (...args: unknown[]) => mockRetryFailedTenants(...args),
}));

jest.mock('@/components/ui/message', () => ({
  __esModule: true,
  default: {
    error: (...args: unknown[]) => mockMessageError(...args),
  },
}));

jest.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      mockFormatTranslation(key, params),
  }),
}));

const STORAGE_KEY = 'modelPlatform.lastRolloutTaskId';

function makeModel(overrides: Record<string, unknown> = {}) {
  return {
    id: 'm-chat-default',
    model_name: 'gpt-4o',
    model_type: 1,
    is_default: true,
    status: 'active',
    tenants: { active: 4, deprecated: 1, tombstoned: 0 },
    active_kb_count: 0,
    active_dialog_count: 5,
    create_time: 1700000000,
    ...overrides,
  };
}

describe('ModelProvidersPage', () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    jest.clearAllMocks();
    mockGetAdminHealth.mockResolvedValue({
      status: 'ok',
      service: 'model-platform',
      gateway: 'healthy',
      latency_ms: 14,
    });
    mockListManagedModels.mockResolvedValue([]);
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it('enforces default/retired action safety rules and shows type-specific confirmation impact', async () => {
    mockListManagedModels.mockResolvedValue([
      makeModel({
        id: 'chat-default',
        model_name: 'gpt-4o',
        model_type: 1,
        is_default: true,
        status: 'active',
        active_dialog_count: 5,
      }),
      makeModel({
        id: 'embed-secondary',
        model_name: 'text-embedding-3-large',
        model_type: 2,
        is_default: false,
        status: 'active',
        active_kb_count: 7,
      }),
      makeModel({
        id: 'tts-retired',
        model_name: 'tts-1',
        model_type: 32,
        is_default: false,
        status: 'retired',
      }),
    ]);
    mockRetireModel.mockResolvedValue({ task_id: 'task-retire-1', total: 4 });
    mockGetRolloutTaskDetail.mockResolvedValue({
      progress: {
        task_id: 'task-retire-1',
        model_name: 'text-embedding-3-large',
        status: 'completed',
        total: 4,
        succeeded: 4,
        pending: 0,
        failed: 0,
        upgraded_users_count: 0,
        preserved_users_count: 0,
      },
      failed_items: { total: 0, page: 1, page_size: 10, items: [] },
    });

    renderWithQueryClient(<ModelProvidersPage />);

    const defaultRow = (await screen.findByText('gpt-4o')).closest('tr')!;
    expect(
      within(defaultRow).queryByRole('button', {
        name: 'modelPlatform.providers.actions.setDefault',
      }),
    ).not.toBeInTheDocument();
    expect(
      within(defaultRow).getByRole('button', {
        name: 'modelPlatform.providers.actions.retire',
      }),
    ).toBeDisabled();

    const retiredRow = screen.getByText('tts-1').closest('tr')!;
    expect(
      within(retiredRow).getByRole('button', {
        name: 'modelPlatform.providers.actions.reactivate',
      }),
    ).toBeEnabled();
    expect(
      within(retiredRow).queryByRole('button', {
        name: 'modelPlatform.providers.actions.retire',
      }),
    ).not.toBeInTheDocument();

    const embedRow = screen.getByText('text-embedding-3-large').closest('tr')!;
    fireEvent.click(
      within(embedRow).getByRole('button', {
        name: 'modelPlatform.providers.actions.retire',
      }),
    );

    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText(
        'modelPlatform.providers.confirmDialog.describeRetireEmbedding(count=7,model=text-embedding-3-large)',
      ),
    ).toBeInTheDocument();

    fireEvent.click(
      within(dialog).getByRole('button', {
        name: 'modelPlatform.providers.confirmDialog.confirm',
      }),
    );

    await waitFor(() =>
      expect(mockRetireModel).toHaveBeenCalledWith('embed-secondary'),
    );
    await waitFor(() =>
      expect(window.sessionStorage.getItem(STORAGE_KEY)).toBe('task-retire-1'),
    );
  });

  it('trims onboard model name, sends chosen model type, stores rollout task, and refetches catalog', async () => {
    mockOnboardModel.mockResolvedValue({ task_id: 'task-onboard-1', total: 3 });
    mockGetRolloutTaskDetail.mockResolvedValue({
      progress: {
        task_id: 'task-onboard-1',
        model_name: 'tts-1-hd',
        status: 'completed',
        total: 3,
        succeeded: 3,
        pending: 0,
        failed: 0,
        upgraded_users_count: 2,
        preserved_users_count: 1,
      },
      failed_items: { total: 0, page: 1, page_size: 10, items: [] },
    });

    renderWithQueryClient(<ModelProvidersPage />);

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'modelPlatform.providers.onboardButton',
      }),
    );

    const dialog = await screen.findByRole('dialog');
    const confirmBtn = within(dialog).getByRole('button', {
      name: 'modelPlatform.providers.onboardDialog.confirm',
    });
    expect(confirmBtn).toBeDisabled();

    const input = within(dialog).getByPlaceholderText(
      'modelPlatform.providers.onboardDialog.placeholder',
    );
    fireEvent.change(input, { target: { value: '   ' } });
    expect(confirmBtn).toBeDisabled();

    fireEvent.change(input, { target: { value: '  tts-1-hd  ' } });
    fireEvent.click(
      within(dialog).getByRole('button', {
        name: 'modelPlatform.modelTypes.tts',
      }),
    );
    expect(confirmBtn).toBeEnabled();

    const initialCatalogCalls = mockListManagedModels.mock.calls.length;
    fireEvent.click(confirmBtn);

    await waitFor(() =>
      expect(mockOnboardModel).toHaveBeenCalledWith('tts-1-hd', 32),
    );
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBe('task-onboard-1');
    await waitFor(() =>
      expect(mockListManagedModels.mock.calls.length).toBeGreaterThan(
        initialCatalogCalls,
      ),
    );
  });

  it('polls running rollout from sessionStorage, stops polling when finished, supports failed pagination, retry, and dismiss', async () => {
    jest.useFakeTimers();
    window.sessionStorage.setItem(STORAGE_KEY, 'task-persisted');

    let pollCount = 0;
    mockGetRolloutTaskDetail.mockImplementation(
      async (_taskId: string, page: number) => {
        pollCount += 1;
        if (pollCount === 1) {
          return {
            progress: {
              task_id: 'task-persisted',
              model_name: 'gpt-4o',
              status: 'running',
              total: 12,
              succeeded: 6,
              pending: 6,
              failed: 0,
              upgraded_users_count: 0,
              preserved_users_count: 0,
            },
            failed_items: { total: 0, page: 1, page_size: 10, items: [] },
          };
        }
        if (page === 2) {
          return {
            progress: {
              task_id: 'task-persisted',
              model_name: 'gpt-4o',
              status: 'partial_failed',
              total: 12,
              succeeded: 1,
              pending: 0,
              failed: 11,
              upgraded_users_count: 1,
              preserved_users_count: 0,
            },
            failed_items: {
              total: 11,
              page: 2,
              page_size: 10,
              items: [
                {
                  tenant_id: 'tenant-11',
                  retry_count: 2,
                  last_error: 'gateway timeout',
                },
              ],
            },
          };
        }
        return {
          progress: {
            task_id: 'task-persisted',
            model_name: 'gpt-4o',
            status: 'partial_failed',
            total: 12,
            succeeded: 1,
            pending: 0,
            failed: 11,
            upgraded_users_count: 1,
            preserved_users_count: 0,
          },
          failed_items: {
            total: 11,
            page: 1,
            page_size: 10,
            items: [
              {
                tenant_id: 'tenant-01',
                retry_count: 1,
                last_error: 'quota exceeded',
              },
            ],
          },
        };
      },
    );
    mockRetryFailedTenants.mockResolvedValue({ retried: 11 });

    renderWithQueryClient(<ModelProvidersPage />);

    expect(
      await screen.findByText(/modelPlatform\.providers\.rollout\.running/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', {
        name: 'modelPlatform.providers.rollout.dismiss',
      }),
    ).not.toBeInTheDocument();

    // Advance past the 3s polling interval so it transitions to partial_failed.
    await act(async () => {
      jest.advanceTimersByTime(3100);
    });

    expect(
      await screen.findByText(/modelPlatform\.providers\.rollout\.finished/),
    ).toBeInTheDocument();
    const callsAfterFinished = mockGetRolloutTaskDetail.mock.calls.length;

    // Polling must stop once status !== 'running'.
    await act(async () => {
      jest.advanceTimersByTime(9000);
    });
    expect(mockGetRolloutTaskDetail.mock.calls.length).toBe(callsAfterFinished);

    // Expand failed items and navigate to page 2.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.providers.rollout.showFailedDetails(count=11)',
      }),
    );
    expect(await screen.findByText('tenant-01')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.pagination.next',
      }),
    );
    expect(await screen.findByText('tenant-11')).toBeInTheDocument();

    // Retry resets back to page 1 and invalidates rollout query.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.providers.actions.retryFailed',
      }),
    );
    await waitFor(() =>
      expect(mockRetryFailedTenants).toHaveBeenCalledWith('task-persisted'),
    );
    await waitFor(() =>
      expect(mockGetRolloutTaskDetail).toHaveBeenLastCalledWith(
        'task-persisted',
        1,
        10,
      ),
    );

    // Dismiss clears banner and sessionStorage.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.providers.rollout.dismiss',
      }),
    );
    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBeNull();
    expect(
      screen.queryByText(/modelPlatform\.providers\.rollout\.finished/),
    ).not.toBeInTheDocument();
  });

  it('renders degraded and unreachable gateway banners and supports manual health refresh', async () => {
    mockGetAdminHealth
      .mockResolvedValueOnce({
        status: 'degraded',
        service: 'model-platform',
        gateway: 'unreachable',
        error: 'gateway dial refused',
        latency_ms: 8,
      })
      .mockRejectedValueOnce(new Error('network down'));

    renderWithQueryClient(<ModelProvidersPage />);

    expect(
      await screen.findByText('modelPlatform.status.gatewayDegraded'),
    ).toBeInTheDocument();
    expect(screen.getByText('gateway dial refused')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.status.refreshHealth',
      }),
    );

    expect(
      await screen.findByText('modelPlatform.status.gatewayUnreachable'),
    ).toBeInTheDocument();
    expect(screen.getByText('network down')).toBeInTheDocument();
  });

  it('shows rollout load error controls and surfaces action mutation errors via toast', async () => {
    window.sessionStorage.setItem(STORAGE_KEY, 'task-broken');
    mockGetRolloutTaskDetail.mockRejectedValue(new Error('task not found'));
    mockListManagedModels.mockResolvedValue([
      makeModel({
        id: 'm-1',
        model_name: 'gpt-4o-mini',
        model_type: 1,
        is_default: false,
        status: 'active',
      }),
    ]);
    mockSetDefaultModel.mockRejectedValue(new Error('default switch blocked'));

    renderWithQueryClient(<ModelProvidersPage />);

    expect(
      await screen.findByText(
        'modelPlatform.providers.rollout.loadFailed(message=task not found)',
      ),
    ).toBeInTheDocument();

    const row = (await screen.findByText('gpt-4o-mini')).closest('tr')!;
    fireEvent.click(
      within(row).getByRole('button', {
        name: 'modelPlatform.providers.actions.setDefault',
      }),
    );
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(
      within(dialog).getByRole('button', {
        name: 'modelPlatform.providers.confirmDialog.confirm',
      }),
    );

    await waitFor(() =>
      expect(mockMessageError).toHaveBeenCalledWith('default switch blocked'),
    );
  });
});
