import { useQuery } from '@tanstack/react-query';
import {
  LucideAudioLines,
  LucideBarChart3,
  LucideCoins,
  LucideTrendingUp,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

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
  getDailyUsage,
  getUsageSummary,
  type DailyUsageItem,
  type UsageQueryParams,
} from '@/ee/utils/model-platform-client';

const PAGE_SIZE = 20;

function formatNumber(value: number): string {
  return value.toLocaleString();
}

function formatSpend(value: number): string {
  return `$${value.toFixed(4)}`;
}

export default function ModelUsageStatisticsPage() {
  const { t } = useTranslation();
  const [tenantId, setTenantId] = useState('');
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');
  const [modelType, setModelType] = useState<number | undefined>(undefined);
  const [page, setPage] = useState(1);

  const baseParams = useMemo<UsageQueryParams>(() => {
    const params: UsageQueryParams = {};
    if (tenantId.trim() !== '') {
      params.tenant_id = tenantId.trim();
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
  }, [tenantId, startDate, endDate, modelType]);

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

  const items = dailyPage?.items ?? [];
  const total = dailyPage?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const handleSelectModelType = (nextType: number | undefined) => {
    setModelType(nextType);
    setPage(1);
  };

  const handleReset = () => {
    setTenantId('');
    setStartDate('');
    setEndDate('');
    setModelType(undefined);
    setPage(1);
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

  return (
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

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
          <Input
            placeholder={t('modelPlatform.usage.filters.tenantPlaceholder')}
            value={tenantId}
            onChange={(event) => {
              setTenantId(event.target.value);
              setPage(1);
            }}
          />
          <Input
            type="date"
            aria-label={t('modelPlatform.usage.filters.startDate')}
            value={startDate}
            onChange={(event) => {
              setStartDate(event.target.value);
              setPage(1);
            }}
          />
          <Input
            type="date"
            aria-label={t('modelPlatform.usage.filters.endDate')}
            value={endDate}
            onChange={(event) => {
              setEndDate(event.target.value);
              setPage(1);
            }}
          />
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
              {t('modelPlatform.usage.cards.selfManaged')}
            </span>
            <LucideTrendingUp className="size-5 text-emerald-500" />
          </div>
          <div className="mt-2 text-2xl font-bold">
            {t('modelPlatform.usage.cards.zeroBilled')}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('modelPlatform.usage.cards.selfManagedHint')}
          </p>
        </div>
      </div>

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
                <TableCell className="font-medium">{row.model_name}</TableCell>
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
                          dailyError instanceof Error ? dailyError.message : '',
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
  );
}
