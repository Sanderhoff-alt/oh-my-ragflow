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
import ModelTenantsPage from './model-tenants';

const mockListTenantKeys = jest.fn();
const mockRotateTenantKey = jest.fn();
const mockAuditTenantUsage = jest.fn();
const mockMessageSuccess = jest.fn();
const mockMessageWarning = jest.fn();
const mockMessageError = jest.fn();

jest.mock('@/ee/utils/model-platform-client', () => ({
  listTenantKeys: (...args: unknown[]) => mockListTenantKeys(...args),
  rotateTenantKey: (...args: unknown[]) => mockRotateTenantKey(...args),
  auditTenantUsage: (...args: unknown[]) => mockAuditTenantUsage(...args),
}));

jest.mock('@/components/ui/message', () => ({
  __esModule: true,
  default: {
    success: (...args: unknown[]) => mockMessageSuccess(...args),
    warning: (...args: unknown[]) => mockMessageWarning(...args),
    error: (...args: unknown[]) => mockMessageError(...args),
  },
}));

jest.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      mockFormatTranslation(key, params),
  }),
}));

function makeTenant(index: number, overrides: Record<string, unknown> = {}) {
  return {
    tenant_id: `tenant-${String(index).padStart(2, '0')}`,
    status: 'ready',
    key_alias: `alias-${index}`,
    key_fingerprint: `fp-${index}`,
    key_rotated_at: '2026-11-20T00:00:00Z',
    key_rotated_by: 'admin',
    ...overrides,
  };
}

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('ModelTenantsPage', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('paginates with after-cursor stack and resets cursor when switching status filter', async () => {
    const firstPageItems = Array.from({ length: 20 }, (_, i) =>
      makeTenant(i + 1),
    );
    const secondPageItems = [makeTenant(21)];
    const degradedItems = [
      makeTenant(99, { status: 'degraded', key_dirty_since: undefined }),
    ];

    mockListTenantKeys.mockImplementation(
      async (params: { status?: string; after?: string }) => {
        if (params.status === 'degraded') {
          return { total: 1, page_size: 20, items: degradedItems };
        }
        if (params.after === 'tenant-20') {
          return { total: 21, page_size: 20, items: secondPageItems };
        }
        return { total: 21, page_size: 20, items: firstPageItems };
      },
    );

    renderWithQueryClient(<ModelTenantsPage />);

    expect(await screen.findByText('tenant-01')).toBeInTheDocument();
    expect(mockListTenantKeys).toHaveBeenCalledWith({
      status: 'ready',
      after: undefined,
      page_size: 20,
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.pagination.next',
      }),
    );
    expect(await screen.findByText('tenant-21')).toBeInTheDocument();
    expect(mockListTenantKeys).toHaveBeenLastCalledWith({
      status: 'ready',
      after: 'tenant-20',
      page_size: 20,
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.pagination.prev',
      }),
    );
    expect(await screen.findByText('tenant-01')).toBeInTheDocument();

    // Advance to page 2 again, then switch status filter; cursor must reset.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.pagination.next',
      }),
    );
    expect(await screen.findByText('tenant-21')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.tenants.statuses.degraded',
      }),
    );
    expect(await screen.findByText('tenant-99')).toBeInTheDocument();
    expect(mockListTenantKeys).toHaveBeenLastCalledWith({
      status: 'degraded',
      after: undefined,
      page_size: 20,
    });
  });

  it('guards key rotation by ready status, distinguishes clean vs dirty resume, and handles converged/pending results', async () => {
    mockListTenantKeys.mockResolvedValue({
      total: 3,
      page_size: 20,
      items: [
        makeTenant(1, { status: 'ready' }),
        makeTenant(2, {
          status: 'ready',
          key_dirty_since: '2026-11-24T10:00:00Z',
        }),
        makeTenant(3, { status: 'failed' }),
      ],
    });
    mockRotateTenantKey
      .mockResolvedValueOnce({
        tenant_id: 'tenant-01',
        key_alias: 'alias-new-1',
        new_key_fingerprint: 'fp-new-1',
        rotated_at: '2026-11-25T00:00:00Z',
        revoked_keys: 1,
        old_keys_revoked: true,
      })
      .mockResolvedValueOnce({
        tenant_id: 'tenant-02',
        key_alias: 'alias-new-2',
        new_key_fingerprint: 'fp-new-2',
        rotated_at: '2026-11-25T00:01:00Z',
        revoked_keys: 0,
        old_keys_revoked: false,
      });

    renderWithQueryClient(<ModelTenantsPage />);

    const failedRow = (await screen.findByText('tenant-03')).closest('tr')!;
    expect(
      within(failedRow).getByRole('button', {
        name: 'modelPlatform.actions.rotateKey',
      }),
    ).toBeDisabled();

    // Clean tenant rotation -> converged success toast + banner
    const cleanRow = screen.getByText('tenant-01').closest('tr')!;
    fireEvent.click(
      within(cleanRow).getByRole('button', {
        name: 'modelPlatform.actions.rotateKey',
      }),
    );
    const cleanDialog = await screen.findByRole('dialog');
    expect(
      within(cleanDialog).getByText('modelPlatform.actions.rotateKeyTitle'),
    ).toBeInTheDocument();
    fireEvent.click(
      within(cleanDialog).getByRole('button', {
        name: 'modelPlatform.actions.rotateKeyConfirm',
      }),
    );

    await waitFor(() =>
      expect(mockRotateTenantKey).toHaveBeenCalledWith('tenant-01'),
    );
    await waitFor(() =>
      expect(mockMessageSuccess).toHaveBeenCalledWith(
        'modelPlatform.actions.rotateKeySuccess(alias=alias-new-1,tenantId=tenant-01)',
      ),
    );
    expect(
      await screen.findByText(
        'modelPlatform.actions.rotateKeyResultConverged(tenantId=tenant-01)',
      ),
    ).toBeInTheDocument();

    // Dirty tenant rotation -> resume copy + pending revocation warning
    const dirtyRow = screen.getByText('tenant-02').closest('tr')!;
    fireEvent.click(
      within(dirtyRow).getByRole('button', {
        name: 'modelPlatform.actions.rotateKeyResume',
      }),
    );
    const dirtyDialog = await screen.findByRole('dialog');
    expect(
      within(dirtyDialog).getByText(
        'modelPlatform.actions.rotateKeyResumeTitle',
      ),
    ).toBeInTheDocument();
    fireEvent.click(
      within(dirtyDialog).getByRole('button', {
        name: 'modelPlatform.actions.rotateKeyConfirm',
      }),
    );

    await waitFor(() =>
      expect(mockRotateTenantKey).toHaveBeenCalledWith('tenant-02'),
    );
    await waitFor(() =>
      expect(mockMessageWarning).toHaveBeenCalledWith(
        'modelPlatform.actions.rotateKeyPendingRevocation(tenantId=tenant-02)',
      ),
    );
    expect(
      await screen.findByText(
        'modelPlatform.actions.rotateKeyResultPending(tenantId=tenant-02)',
      ),
    ).toBeInTheDocument();
  });

  it('ignores stale isolation audit responses after closing or switching tenants', async () => {
    mockListTenantKeys.mockResolvedValue({
      total: 2,
      page_size: 20,
      items: [makeTenant(1), makeTenant(2)],
    });

    const firstAudit = createDeferred<any>();
    const secondAudit = createDeferred<any>();
    mockAuditTenantUsage
      .mockReturnValueOnce(firstAudit.promise)
      .mockReturnValueOnce(secondAudit.promise);

    renderWithQueryClient(<ModelTenantsPage />);

    const row1 = (await screen.findByText('tenant-01')).closest('tr')!;
    fireEvent.click(
      within(row1).getByRole('button', {
        name: 'modelPlatform.audit.runAction',
      }),
    );
    const dialog1 = await screen.findByRole('dialog');
    expect(
      within(dialog1).getByText(
        'modelPlatform.audit.dialogSubtitle(tenantId=tenant-01)',
      ),
    ).toBeInTheDocument();

    // Close dialog while tenant-01 audit is still in flight, then open tenant-02.
    fireEvent.click(
      within(dialog1).getByRole('button', {
        name: 'modelPlatform.audit.close',
      }),
    );
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    const row2 = screen.getByText('tenant-02').closest('tr')!;
    fireEvent.click(
      within(row2).getByRole('button', {
        name: 'modelPlatform.audit.runAction',
      }),
    );
    const dialog2 = await screen.findByRole('dialog');

    // Late resolution from tenant-01 must be discarded.
    await act(async () => {
      firstAudit.resolve({
        tenant_id: 'tenant-01',
        managed_spend_total: 9.99,
        self_owned_spend_total: 0,
        audit_passed: true,
      });
    });

    expect(
      within(dialog2).queryByText('modelPlatform.audit.passed'),
    ).not.toBeInTheDocument();

    // Resolution from tenant-02 renders violation details.
    await act(async () => {
      secondAudit.resolve({
        tenant_id: 'tenant-02',
        managed_spend_total: 1.25,
        self_owned_spend_total: 0.5,
        audit_passed: false,
        unmanaged_models_detected: ['rogue-model'],
        violations: ['unmanaged spend detected'],
      });
    });

    expect(
      await within(dialog2).findByText('modelPlatform.audit.violationDetected'),
    ).toBeInTheDocument();
    expect(within(dialog2).getByText('rogue-model')).toBeInTheDocument();
    expect(
      within(dialog2).getByText('unmanaged spend detected'),
    ).toBeInTheDocument();
  });

  it('displays list load error state and retries on demand', async () => {
    mockListTenantKeys
      .mockRejectedValueOnce(new Error('backend unavailable'))
      .mockResolvedValueOnce({
        total: 1,
        page_size: 20,
        items: [makeTenant(1)],
      });

    renderWithQueryClient(<ModelTenantsPage />);

    expect(
      await screen.findByText(
        'modelPlatform.tenants.loadFailed(message=backend unavailable)',
      ),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', { name: 'modelPlatform.usage.error.retry' }),
    );
    expect(await screen.findByText('tenant-01')).toBeInTheDocument();
  });
});
