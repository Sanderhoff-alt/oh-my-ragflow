import { useMutation, useQuery } from '@tanstack/react-query';
import {
  LucideAudioLines,
  LucideBarChart3,
  LucideCoins,
  LucideShieldAlert,
  LucideShieldCheck,
} from 'lucide-react';
import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  getModelTypeSpec,
  MANAGED_MODEL_TYPES,
} from '@/ee/constants/model-types';
import {
  auditTenantUsage,
  getDailyUsage,
  getUsageSummary,
  type AuditIsolationReport,
  type DailyUsageItem,
  type UsageQueryParams,
} from '@/ee/utils/model-platform-client';

const PAGE_SIZE = 20;
const TREND_PAGE_SIZE = 100;

type TrendMetric = 'spend' | 'tokens';

type DailyTrendPoint = {
  date: string;
  spend: number;
  tokens: number;
  calls: number;
};

function formatNumber(value: number): string {
  return value.toLocaleString();
}

function formatSpend(value: number): string {
  return `$${value.toFixed(4)}`;
}

function formatCompactNumber(value: number): string {
  if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1)}M`;
  }
  if (value >= 1_000) {
    return `${(value / 1_000).toFixed(1)}K`;
  }
  return String(value);
}

export default function ModelUsageStatisticsPage() {
  const { t } = useTranslation();
  const [tenantId, setTenantId] = useState('');
  const [modelName, setModelName] = useState('');
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');
  const [modelType, setModelType] = useState<number | undefined>(undefined);
  const [page, setPage] = useState(1);
  const [trendMetric, setTrendMetric] = useState<TrendMetric>('spend');
  const [auditReport, setAuditReport] = useState<AuditIsolationReport | null>(
    null,
  );
  const [auditError, setAuditError] = useState<{
    tenantId: string;
    message: string;
  } | null>(null);
  const auditGenerationRef = useRef(0);

  const trimmedTenantId = tenantId.trim();

  const baseParams = useMemo<UsageQueryParams>(() => {
    const params: UsageQueryParams = {};
    if (trimmedTenantId !== '') {
      params.tenant_id = trimmedTenantId;
    }
    if (modelName.trim() !== '') {
      params.model_name = modelName.trim();
    }
    if (startDate !== '') {
      params.start_date = startDate;
    }
    if (endDate !== '') {
      params.end_date = endDate;
    }
    if (modelType !== undefined) {
      params.model_type = modelType;
    }
    return params;
  }, [trimmedTenantId, modelName, startDate, endDate, modelType]);

  const {
    data: summary,
    error: summaryError,
    isError: isSummaryError,
    refetch: refetchSummary,
  } = useQuery({
    queryKey: ['model-platform', 'usage', 'summary', baseParams],
    queryFn: () => getUsageSummary(baseParams),
  });

  const {
    data: dailyPage,
    error: dailyError,
    isError: isDailyError,
    isLoading,
    refetch: refetchDaily,
  } = useQuery({
    queryKey: ['model-platform', 'usage', 'daily', baseParams, page],
    queryFn: () =>
      getDailyUsage({
        ...baseParams,
        page,
        page_size: PAGE_SIZE,
      }),
  });

  const {
    data: trendPage,
    error: trendError,
    isError: isTrendError,
    refetch: refetchTrend,
  } = useQuery({
    queryKey: ['model-platform', 'usage', 'trend', baseParams],
    queryFn: () =>
      getDailyUsage({
        ...baseParams,
        page: 1,
        page_size: TREND_PAGE_SIZE,
      }),
  });

  const auditMutation = useMutation({
    mutationFn: ({
      targetTenant,
    }: {
      targetTenant: string;
      generation: number;
    }) => auditTenantUsage(targetTenant),
    onSuccess: (report, variables) => {
      if (variables.generation !== auditGenerationRef.current) {
        return;
      }
      setAuditReport(report);
      setAuditError(null);
    },
    onError: (err: Error, variables) => {
      if (variables.generation !== auditGenerationRef.current) {
        return;
      }
      setAuditReport((prev) =>
        prev?.tenant_id === variables.targetTenant ? null : prev,
      );
      setAuditError({ tenantId: variables.targetTenant, message: err.message });
    },
  });

  const items = dailyPage?.items ?? [];
  const total = dailyPage?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const trendData = useMemo<DailyTrendPoint[]>(() => {
    const rows = trendPage?.items ?? [];
    const byDate = new Map<string, DailyTrendPoint>();
    for (const row of rows) {
      const existing = byDate.get(row.usage_date);
      if (existing) {
        existing.spend += row.total_spend;
        existing.tokens += row.total_tokens;
        existing.calls += row.call_count;
      } else {
        byDate.set(row.usage_date, {
          date: row.usage_date,
          spend: row.total_spend,
          tokens: row.total_tokens,
          calls: row.call_count,
        });
      }
    }
    return Array.from(byDate.values()).sort((a, b) =>
      a.date.localeCompare(b.date),
    );
  }, [trendPage]);

  const isTrendTruncated =
    trendPage !== undefined && trendPage.total > TREND_PAGE_SIZE;

  const activeAudit =
    auditReport && auditReport.tenant_id === trimmedTenantId
      ? auditReport
      : null;

  const activeAuditError =
    auditError && auditError.tenantId === trimmedTenantId
      ? auditError.message
      : null;

  const isRunningAuditForCurrentTenant =
    auditMutation.isPending &&
    auditMutation.variables?.targetTenant === trimmedTenantId;

  const handleRunAudit = () => {
    if (trimmedTenantId === '') {
      return;
    }
    const generation = ++auditGenerationRef.current;
    setAuditReport((prev) =>
      prev?.tenant_id === trimmedTenantId ? null : prev,
    );
    setAuditError((prev) => (prev?.tenantId === trimmedTenantId ? null : prev));
    auditMutation.mutate({ targetTenant: trimmedTenantId, generation });
  };

  const handleSelectModelType = (nextType: number | undefined) => {
    setModelType(nextType);
    setPage(1);
  };

  const handleReset = () => {
    auditGenerationRef.current += 1;
    auditMutation.reset();
    setTenantId('');
    setModelName('');
    setStartDate('');
    setEndDate('');
    setModelType(undefined);
    setPage(1);
    setAuditReport(null);
    setAuditError(null);
  };

  const renderModelTypeLabel = (bit: number) => {
    const spec = getModelTypeSpec(bit);
    return spec
      ? t(`modelPlatform.modelTypes.${spec.key}`)
      : t('modelPlatform.modelTypes.unknown');
  };

  const renderUsageMetric = (row: DailyUsageItem) => {
    const spec = getModelTypeSpec(row.model_type);
    if (spec?.metricUnit === 'characters' || row.characters > 0) {
      return t('modelPlatform.usage.table.charactersUnit', {
        value: formatNumber(row.characters),
      });
    }
    return t('modelPlatform.usage.table.tokensUnit', {
      value: formatNumber(row.total_tokens),
    });
  };

  const hasAdvancedTokens =
    (summary?.total_cached_tokens ?? 0) > 0 ||
    (summary?.total_reasoning_tokens ?? 0) > 0;
  const hasCacheSavings = (summary?.total_cache_savings ?? 0) > 0;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-7xl space-y-6 p-8">
        <div className="flex items-center justify-between border-b pb-4">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">
              {t('modelPlatform.usage.title')}
            </h1>
            <p className="mt-1 text-sm text-muted-foreground">
              {t('modelPlatform.usage.subtitle')}
            </p>
          </div>
        </div>

        <div className="space-y-4 rounded-xl border bg-card p-4 shadow-sm">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              {t('modelPlatform.usage.filters.modelType')}:
            </span>
            <Button
              size="sm"
              variant={modelType === undefined ? 'default' : 'secondary'}
              onClick={() => handleSelectModelType(undefined)}
            >
              {t('modelPlatform.modelTypes.all')}
            </Button>
            {MANAGED_MODEL_TYPES.map((spec) => (
              <Button
                key={spec.bit}
                size="sm"
                variant={modelType === spec.bit ? 'default' : 'secondary'}
                onClick={() => handleSelectModelType(spec.bit)}
              >
                {t(`modelPlatform.modelTypes.${spec.key}`)}
              </Button>
            ))}
          </div>

          <div className="grid grid-cols-1 items-end gap-3 sm:grid-cols-5">
            <div className="space-y-1">
              <label
                htmlFor="usage-filter-tenant"
                className="block text-xs font-medium text-muted-foreground"
              >
                {t('modelPlatform.usage.filters.tenantLabel')}
              </label>
              <Input
                id="usage-filter-tenant"
                placeholder={t('modelPlatform.usage.filters.tenantPlaceholder')}
                value={tenantId}
                onChange={(event) => {
                  setTenantId(event.target.value);
                  setPage(1);
                }}
              />
            </div>
            <div className="space-y-1">
              <label
                htmlFor="usage-filter-model"
                className="block text-xs font-medium text-muted-foreground"
              >
                {t('modelPlatform.usage.filters.modelLabel')}
              </label>
              <Input
                id="usage-filter-model"
                placeholder={t('modelPlatform.usage.filters.modelPlaceholder')}
                value={modelName}
                onChange={(event) => {
                  setModelName(event.target.value);
                  setPage(1);
                }}
              />
            </div>
            <div className="space-y-1">
              <label
                htmlFor="usage-filter-start-date"
                className="block text-xs font-medium text-muted-foreground"
              >
                {t('modelPlatform.usage.filters.startDate')}
              </label>
              <Input
                id="usage-filter-start-date"
                type="date"
                value={startDate}
                onChange={(event) => {
                  setStartDate(event.target.value);
                  setPage(1);
                }}
              />
            </div>
            <div className="space-y-1">
              <label
                htmlFor="usage-filter-end-date"
                className="block text-xs font-medium text-muted-foreground"
              >
                {t('modelPlatform.usage.filters.endDate')}
              </label>
              <Input
                id="usage-filter-end-date"
                type="date"
                value={endDate}
                onChange={(event) => {
                  setEndDate(event.target.value);
                  setPage(1);
                }}
              />
            </div>
            <div className="flex justify-end">
              <Button variant="secondary" onClick={handleReset}>
                {t('modelPlatform.usage.filters.reset')}
              </Button>
            </div>
          </div>
        </div>

        {isSummaryError && (
          <div className="flex items-center justify-between rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            <span>
              {t('modelPlatform.usage.error.summaryFailed', {
                message:
                  summaryError instanceof Error ? summaryError.message : '',
              })}
            </span>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => refetchSummary()}
            >
              {t('modelPlatform.usage.error.retry')}
            </Button>
          </div>
        )}

        <div className="grid grid-cols-1 gap-6 md:grid-cols-4">
          <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground">
                {t('modelPlatform.usage.cards.totalSpend')}
              </span>
              <LucideCoins className="size-5 text-amber-500" />
            </div>
            <div className="mt-2 text-2xl font-bold">
              {summary ? formatSpend(summary.total_spend) : '—'}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('modelPlatform.usage.cards.totalCalls', {
                value: formatNumber(summary?.total_calls ?? 0),
              })}
            </p>
            {hasCacheSavings && (
              <p className="mt-0.5 text-xs text-emerald-600 dark:text-emerald-400">
                {t('modelPlatform.usage.cards.cacheSavings', {
                  value: formatSpend(summary?.total_cache_savings ?? 0),
                })}
              </p>
            )}
          </div>

          <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground">
                {t('modelPlatform.usage.cards.totalTokens')}
              </span>
              <LucideBarChart3 className="size-5 text-blue-500" />
            </div>
            <div className="mt-2 text-2xl font-bold">
              {summary ? formatNumber(summary.total_tokens) : '—'}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('modelPlatform.usage.cards.tokenSplit', {
                prompt: formatNumber(summary?.total_prompt_tokens ?? 0),
                completion: formatNumber(summary?.total_completion_tokens ?? 0),
              })}
            </p>
            {hasAdvancedTokens && (
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t('modelPlatform.usage.cards.advancedTokens', {
                  cached: formatNumber(summary?.total_cached_tokens ?? 0),
                  reasoning: formatNumber(summary?.total_reasoning_tokens ?? 0),
                })}
              </p>
            )}
          </div>

          <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground">
                {t('modelPlatform.usage.cards.totalCharacters')}
              </span>
              <LucideAudioLines className="size-5 text-purple-500" />
            </div>
            <div className="mt-2 text-2xl font-bold">
              {summary ? formatNumber(summary.total_characters) : '—'}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('modelPlatform.usage.cards.charactersHint')}
            </p>
          </div>

          <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground">
                {t('modelPlatform.usage.cards.isolationAudit')}
              </span>
              {activeAudit && !activeAudit.audit_passed ? (
                <LucideShieldAlert className="size-5 text-destructive" />
              ) : (
                <LucideShieldCheck className="size-5 text-emerald-500" />
              )}
            </div>
            <div className="mt-2 flex items-center justify-between gap-2">
              {isRunningAuditForCurrentTenant ? (
                <span className="text-sm font-medium text-muted-foreground">
                  {t('modelPlatform.audit.running')}
                </span>
              ) : activeAudit ? (
                <Badge
                  variant={activeAudit.audit_passed ? 'success' : 'destructive'}
                >
                  {activeAudit.audit_passed
                    ? t('modelPlatform.audit.passed')
                    : t('modelPlatform.audit.violationDetected')}
                </Badge>
              ) : (
                <span className="text-lg font-semibold text-muted-foreground">
                  {trimmedTenantId
                    ? t('modelPlatform.audit.notRunYet')
                    : t('modelPlatform.audit.selectTenantPrompt')}
                </span>
              )}
              <Button
                size="sm"
                variant="secondary"
                disabled={trimmedTenantId === ''}
                loading={isRunningAuditForCurrentTenant}
                onClick={handleRunAudit}
              >
                {t('modelPlatform.audit.runAction')}
              </Button>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {isRunningAuditForCurrentTenant
                ? t('modelPlatform.audit.running')
                : activeAudit
                  ? t('modelPlatform.audit.cardSpendSummary', {
                      managed: formatSpend(activeAudit.managed_spend_total),
                      selfOwned: formatSpend(
                        activeAudit.self_owned_spend_total,
                      ),
                    })
                  : t('modelPlatform.audit.cardHint')}
            </p>
          </div>
        </div>

        {activeAuditError && (
          <div className="rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            {t('modelPlatform.audit.failed', { message: activeAuditError })}
          </div>
        )}

        {activeAudit && !activeAudit.audit_passed && (
          <div className="space-y-2 rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
            <div className="flex items-center gap-2 font-semibold">
              <LucideShieldAlert className="size-4 shrink-0" />
              {t('modelPlatform.audit.alertBannerTitle', {
                tenantId: activeAudit.tenant_id,
              })}
            </div>
            {activeAudit.unmanaged_models_detected &&
              activeAudit.unmanaged_models_detected.length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-xs font-medium">
                    {t('modelPlatform.audit.unmanagedModels')}:
                  </span>
                  {activeAudit.unmanaged_models_detected.map((model) => (
                    <Badge key={model} variant="destructive">
                      {model}
                    </Badge>
                  ))}
                </div>
              )}
            {activeAudit.violations && activeAudit.violations.length > 0 && (
              <ul className="space-y-1 font-mono text-xs">
                {activeAudit.violations.map((v) => (
                  <li key={v}>{v}</li>
                ))}
              </ul>
            )}
          </div>
        )}

        {isTrendError ? (
          <div className="flex items-center justify-between rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            <span>
              {t('modelPlatform.usage.error.trendFailed', {
                message: trendError instanceof Error ? trendError.message : '',
              })}
            </span>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => refetchTrend()}
            >
              {t('modelPlatform.usage.error.retry')}
            </Button>
          </div>
        ) : (
          trendData.length > 0 && (
            <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-sm">
              <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
                <div>
                  <h2 className="text-base font-semibold">
                    {t('modelPlatform.usage.trend.title')}
                  </h2>
                  <p className="text-xs text-muted-foreground">
                    {trendPage && trendPage.total > TREND_PAGE_SIZE
                      ? t('modelPlatform.usage.trend.cappedHint', {
                          shown: TREND_PAGE_SIZE,
                          total: trendPage.total,
                        })
                      : t('modelPlatform.usage.trend.subtitle')}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    variant={trendMetric === 'spend' ? 'default' : 'secondary'}
                    onClick={() => setTrendMetric('spend')}
                  >
                    {t('modelPlatform.usage.trend.spendSeries')}
                  </Button>
                  <Button
                    size="sm"
                    variant={trendMetric === 'tokens' ? 'default' : 'secondary'}
                    onClick={() => setTrendMetric('tokens')}
                  >
                    {t('modelPlatform.usage.trend.tokensSeries')}
                  </Button>
                </div>
              </div>
              <div className="h-56 w-full">
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart
                    data={trendData}
                    margin={{ top: 8, right: 16, left: 8, bottom: 0 }}
                  >
                    <CartesianGrid strokeDasharray="3 3" opacity={0.25} />
                    <XAxis dataKey="date" tick={{ fontSize: 12 }} />
                    <YAxis
                      tick={{ fontSize: 12 }}
                      tickFormatter={(value: number) =>
                        trendMetric === 'spend'
                          ? `$${value.toFixed(2)}`
                          : formatCompactNumber(value)
                      }
                    />
                    <Tooltip
                      formatter={(value: number) => {
                        const formatted =
                          trendMetric === 'spend'
                            ? formatSpend(value)
                            : formatNumber(value);
                        const seriesLabel =
                          trendMetric === 'spend'
                            ? t('modelPlatform.usage.trend.spendSeries')
                            : t('modelPlatform.usage.trend.tokensSeries');
                        return [
                          formatted,
                          isTrendTruncated
                            ? t(
                                'modelPlatform.usage.trend.partialSeriesLabel',
                                {
                                  series: seriesLabel,
                                },
                              )
                            : seriesLabel,
                        ];
                      }}
                    />
                    <Line
                      type="monotone"
                      dataKey={trendMetric}
                      stroke={trendMetric === 'spend' ? '#f59e0b' : '#3b82f6'}
                      strokeWidth={2}
                      dot={{ r: 3 }}
                      activeDot={{ r: 5 }}
                    />
                  </LineChart>
                </ResponsiveContainer>
              </div>
            </div>
          )
        )}

        <div className="overflow-hidden rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between border-b p-6">
            <div>
              <h2 className="text-lg font-semibold">
                {t('modelPlatform.usage.table.title')}
              </h2>
              <p className="text-sm text-muted-foreground">
                {t('modelPlatform.usage.table.subtitle')}
              </p>
            </div>
            <Badge variant="secondary">
              {t('modelPlatform.usage.table.totalRecords', {
                value: formatNumber(total),
              })}
            </Badge>
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  {t('modelPlatform.usage.table.columns.date')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.usage.table.columns.tenant')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.usage.table.columns.model')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.usage.table.columns.type')}
                </TableHead>
                <TableHead className="text-right">
                  {t('modelPlatform.usage.table.columns.calls')}
                </TableHead>
                <TableHead className="text-right">
                  {t('modelPlatform.usage.table.columns.usage')}
                </TableHead>
                <TableHead className="text-right">
                  {t('modelPlatform.usage.table.columns.spend')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((row) => (
                <TableRow
                  key={`${row.usage_date}-${row.tenant_id}-${row.model_name}`}
                >
                  <TableCell>{row.usage_date}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {row.tenant_id}
                  </TableCell>
                  <TableCell className="font-medium">
                    {row.model_name}
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">
                      {renderModelTypeLabel(row.model_type)}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    {formatNumber(row.call_count)}
                  </TableCell>
                  <TableCell className="text-right">
                    {renderUsageMetric(row)}
                  </TableCell>
                  <TableCell className="text-right">
                    {formatSpend(row.total_spend)}
                  </TableCell>
                </TableRow>
              ))}
              {isDailyError ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-8 text-center">
                    <div className="flex flex-col items-center justify-center gap-3 text-sm text-destructive">
                      <span>
                        {t('modelPlatform.usage.error.dailyFailed', {
                          message:
                            dailyError instanceof Error
                              ? dailyError.message
                              : '',
                        })}
                      </span>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => refetchDaily()}
                      >
                        {t('modelPlatform.usage.error.retry')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ) : (
                !isLoading &&
                items.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={7}
                      className="py-8 text-center text-muted-foreground"
                    >
                      {t('modelPlatform.usage.table.empty')}
                    </TableCell>
                  </TableRow>
                )
              )}
            </TableBody>
          </Table>
          {totalPages > 1 && (
            <div className="flex items-center justify-between border-t px-6 py-3 text-sm">
              <span className="text-muted-foreground">
                {t('modelPlatform.usage.pagination.pageInfo', {
                  page,
                  totalPages,
                })}
              </span>
              <div className="space-x-2">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={page <= 1}
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                >
                  {t('modelPlatform.usage.pagination.prev')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={page >= totalPages}
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                >
                  {t('modelPlatform.usage.pagination.next')}
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
