import { act, fireEvent, screen, waitFor } from '@testing-library/react';

import {
  formatTestTranslation as mockFormatTranslation,
  renderWithQueryClient,
} from '@/ee/__test-utils__/query-test-utils';
import ModelUsageStatisticsPage from './model-usage-statistics';

const mockGetUsageSummary = jest.fn();
const mockGetDailyUsage = jest.fn();
const mockAuditTenantUsage = jest.fn();
let mockLineChartData: unknown[] = [];
let mockLineDataKey: string | undefined;

jest.mock('@/ee/utils/model-platform-client', () => ({
  getUsageSummary: (...args: unknown[]) => mockGetUsageSummary(...args),
  getDailyUsage: (...args: unknown[]) => mockGetDailyUsage(...args),
  auditTenantUsage: (...args: unknown[]) => mockAuditTenantUsage(...args),
}));

jest.mock('recharts', () => ({
  ResponsiveContainer: ({ children }: { children?: unknown }) => (
    <div data-testid="recharts-responsive">{children as any}</div>
  ),
  LineChart: ({ data, children }: { data: unknown[]; children?: unknown }) => {
    mockLineChartData = data;
    return <div data-testid="recharts-line-chart">{children as any}</div>;
  },
  Line: ({ dataKey }: { dataKey: string }) => {
    mockLineDataKey = dataKey;
    return <div data-testid={`recharts-line-${dataKey}`} />;
  },
  CartesianGrid: () => null,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

jest.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      mockFormatTranslation(key, params),
  }),
}));

function makeSummary(overrides: Record<string, unknown> = {}) {
  return {
    tenant_id: '',
    total_calls: 120,
    total_prompt_tokens: 10000,
    total_completion_tokens: 5000,
    total_tokens: 15000,
    total_cached_tokens: 0,
    total_cache_write_tokens: 0,
    total_reasoning_tokens: 0,
    total_characters: 2500,
    total_spend: 1.2345,
    total_prompt_spend: 0.8,
    total_completion_spend: 0.4345,
    total_cache_savings: 0,
    model_tokens: {},
    ...overrides,
  };
}

function makeDailyItem(overrides: Record<string, unknown> = {}) {
  return {
    tenant_id: 'tenant-a',
    model_name: 'gpt-4o',
    model_type: 1,
    usage_date: '2026-11-24',
    call_count: 10,
    prompt_tokens: 800,
    completion_tokens: 200,
    total_tokens: 1000,
    cached_tokens: 0,
    cache_write_tokens: 0,
    reasoning_tokens: 0,
    characters: 0,
    total_spend: 0.25,
    prompt_spend: 0.15,
    completion_spend: 0.1,
    cache_savings: 0,
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

describe('ModelUsageStatisticsPage', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockLineChartData = [];
    mockLineDataKey = undefined;
    mockGetUsageSummary.mockResolvedValue(makeSummary());
    mockGetDailyUsage.mockResolvedValue({
      total: 0,
      page: 1,
      page_size: 20,
      items: [],
    });
  });

  it('aggregates daily trend rows by date in ascending order, flags truncation, and switches metric series', async () => {
    const items = [
      makeDailyItem({
        tenant_id: 'tenant-b',
        usage_date: '2026-11-24',
        total_spend: 1.5,
        total_tokens: 300,
        call_count: 3,
      }),
      makeDailyItem({
        tenant_id: 'tenant-a',
        model_name: 'tts-1',
        model_type: 32,
        usage_date: '2026-11-22',
        total_spend: 0.5,
        total_tokens: 0,
        characters: 4200,
        call_count: 2,
      }),
      makeDailyItem({
        tenant_id: 'tenant-a',
        usage_date: '2026-11-24',
        total_spend: 2.0,
        total_tokens: 700,
        call_count: 5,
      }),
    ];

    mockGetUsageSummary.mockResolvedValue(
      makeSummary({
        total_cached_tokens: 500,
        total_reasoning_tokens: 120,
        total_cache_savings: 0.12,
      }),
    );
    mockGetDailyUsage.mockImplementation(
      async (params: { page_size?: number }) => {
        if (params.page_size === 100) {
          return { total: 150, page: 1, page_size: 100, items };
        }
        return { total: 3, page: 1, page_size: 20, items };
      },
    );

    renderWithQueryClient(<ModelUsageStatisticsPage />);

    // TTS row displays characters unit; chat row displays tokens unit.
    expect(
      await screen.findByText(
        'modelPlatform.usage.table.charactersUnit(value=4,200)',
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText('modelPlatform.usage.table.tokensUnit(value=300)'),
    ).toBeInTheDocument();

    // Advanced token and cache savings details appear only when > 0.
    expect(
      screen.getByText('modelPlatform.usage.cards.cacheSavings(value=$0.1200)'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        'modelPlatform.usage.cards.advancedTokens(cached=500,reasoning=120)',
      ),
    ).toBeInTheDocument();

    // Trend truncation hint and aggregated sorted chart data.
    expect(
      screen.getByText(
        'modelPlatform.usage.trend.cappedHint(shown=100,total=150)',
      ),
    ).toBeInTheDocument();
    expect(mockLineChartData).toEqual([
      { date: '2026-11-22', spend: 0.5, tokens: 0, calls: 2 },
      { date: '2026-11-24', spend: 3.5, tokens: 1000, calls: 8 },
    ]);
    expect(mockLineDataKey).toBe('spend');

    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.trend.tokensSeries',
      }),
    );
    expect(mockLineDataKey).toBe('tokens');
  });

  it('trims filters, resets pagination to page 1 on filter changes, and clears everything on Reset', async () => {
    mockGetDailyUsage.mockImplementation(
      async (params: { page?: number; page_size?: number }) => ({
        total: 45,
        page: params.page ?? 1,
        page_size: params.page_size ?? 20,
        items: [makeDailyItem({ tenant_id: `page-${params.page ?? 1}` })],
      }),
    );

    renderWithQueryClient(<ModelUsageStatisticsPage />);

    expect(await screen.findByText('page-1')).toBeInTheDocument();

    // Move to page 2.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'modelPlatform.usage.pagination.next',
      }),
    );
    expect(await screen.findByText('page-2')).toBeInTheDocument();

    // Changing tenant filter trims value and resets page back to 1.
    const tenantInput = screen.getByLabelText(
      'modelPlatform.usage.filters.tenantLabel',
    );
    fireEvent.change(tenantInput, { target: { value: '  tenant-xyz  ' } });

    await waitFor(() =>
      expect(mockGetUsageSummary).toHaveBeenLastCalledWith({
        tenant_id: 'tenant-xyz',
      }),
    );
    await waitFor(() =>
      expect(mockGetDailyUsage).toHaveBeenCalledWith({
        tenant_id: 'tenant-xyz',
        page: 1,
        page_size: 20,
      }),
    );

    // Selecting TTS model type sends model_type=32.
    fireEvent.click(
      screen.getByRole('button', { name: 'modelPlatform.modelTypes.tts' }),
    );
    await waitFor(() =>
      expect(mockGetUsageSummary).toHaveBeenLastCalledWith({
        tenant_id: 'tenant-xyz',
        model_type: 32,
      }),
    );

    // Reset clears filters back to empty params.
    fireEvent.click(
      screen.getByRole('button', { name: 'modelPlatform.usage.filters.reset' }),
    );
    await waitFor(() =>
      expect(mockGetUsageSummary).toHaveBeenLastCalledWith({}),
    );
    expect((tenantInput as HTMLInputElement).value).toBe('');
  });

  it('guards inline tenant audit against empty tenantId and discards out-of-order responses', async () => {
    const auditA = createDeferred<any>();
    const auditB = createDeferred<any>();
    mockAuditTenantUsage
      .mockReturnValueOnce(auditA.promise)
      .mockReturnValueOnce(auditB.promise);

    renderWithQueryClient(<ModelUsageStatisticsPage />);

    const auditButton = await screen.findByRole('button', {
      name: 'modelPlatform.audit.runAction',
    });
    expect(auditButton).toBeDisabled();

    const tenantInput = screen.getByLabelText(
      'modelPlatform.usage.filters.tenantLabel',
    );
    fireEvent.change(tenantInput, { target: { value: ' tenant-a ' } });
    expect(auditButton).toBeEnabled();

    fireEvent.click(auditButton);
    await waitFor(() =>
      expect(mockAuditTenantUsage).toHaveBeenCalledWith('tenant-a'),
    );

    // Switch tenant and trigger a second audit while tenant-a is still pending.
    fireEvent.change(tenantInput, { target: { value: 'tenant-b' } });
    fireEvent.click(auditButton);
    await waitFor(() =>
      expect(mockAuditTenantUsage).toHaveBeenCalledWith('tenant-b'),
    );

    // tenant-b resolves first with a violation -> alert banner shown for tenant-b.
    await act(async () => {
      auditB.resolve({
        tenant_id: 'tenant-b',
        managed_spend_total: 2,
        self_owned_spend_total: 1,
        audit_passed: false,
        unmanaged_models_detected: ['shadow-llm'],
        violations: ['self-owned spend billed'],
      });
    });

    expect(
      await screen.findByText('modelPlatform.audit.violationDetected'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        'modelPlatform.audit.alertBannerTitle(tenantId=tenant-b)',
      ),
    ).toBeInTheDocument();
    expect(screen.getByText('shadow-llm')).toBeInTheDocument();

    // Stale tenant-a result resolves after tenant-b -> must be rejected so
    // tenant-b's report stays visible instead of being overwritten.
    await act(async () => {
      auditA.resolve({
        tenant_id: 'tenant-a',
        managed_spend_total: 10,
        self_owned_spend_total: 0,
        audit_passed: true,
      });
    });

    expect(
      screen.getByText('modelPlatform.audit.violationDetected'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        'modelPlatform.audit.alertBannerTitle(tenantId=tenant-b)',
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('modelPlatform.audit.passed'),
    ).not.toBeInTheDocument();

    // Changing tenant hides tenant-b's audit report immediately.
    fireEvent.change(tenantInput, { target: { value: 'tenant-c' } });
    expect(
      screen.queryByText('modelPlatform.audit.violationDetected'),
    ).not.toBeInTheDocument();
  });

  it('isolates summary, trend, and daily query failures with independent retry controls', async () => {
    mockGetUsageSummary
      .mockRejectedValueOnce(new Error('summary boom'))
      .mockResolvedValueOnce(makeSummary());
    mockGetDailyUsage.mockImplementation(
      async (params: { page_size?: number }) => {
        if (params.page_size === 100) {
          throw new Error('trend boom');
        }
        throw new Error('daily boom');
      },
    );

    renderWithQueryClient(<ModelUsageStatisticsPage />);

    expect(
      await screen.findByText(
        'modelPlatform.usage.error.summaryFailed(message=summary boom)',
      ),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        'modelPlatform.usage.error.trendFailed(message=trend boom)',
      ),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        'modelPlatform.usage.error.dailyFailed(message=daily boom)',
      ),
    ).toBeInTheDocument();

    const retryButtons = screen.getAllByRole('button', {
      name: 'modelPlatform.usage.error.retry',
    });
    expect(retryButtons).toHaveLength(3);

    // Retrying summary clears the summary error without affecting daily/trend errors.
    fireEvent.click(retryButtons[0]);
    await waitFor(() =>
      expect(
        screen.queryByText(
          'modelPlatform.usage.error.summaryFailed(message=summary boom)',
        ),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.getByText(
        'modelPlatform.usage.error.dailyFailed(message=daily boom)',
      ),
    ).toBeInTheDocument();
  });
});
