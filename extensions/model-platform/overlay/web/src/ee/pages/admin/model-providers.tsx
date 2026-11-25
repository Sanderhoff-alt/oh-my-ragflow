import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  LucideActivity,
  LucideAlertTriangle,
  LucideBox,
  LucideChevronDown,
  LucideChevronUp,
  LucidePlus,
  LucideRefreshCw,
  LucideStar,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import message from '@/components/ui/message';
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
  getAdminHealth,
  getRolloutTaskDetail,
  listManagedModels,
  onboardModel,
  reactivateModel,
  retireModel,
  retryFailedTenants,
  setDefaultModel,
  type ManagedModel,
  type RolloutResult,
} from '@/ee/utils/model-platform-client';

const LAST_ROLLOUT_TASK_STORAGE_KEY = 'modelPlatform.lastRolloutTaskId';
const FAILED_ITEMS_PAGE_SIZE = 10;

type Action = 'retire' | 'reactivate' | 'default';

type PendingAction = { action: Action; model: ManagedModel };

function readStoredTaskId(): string | null {
  try {
    return window.sessionStorage.getItem(LAST_ROLLOUT_TASK_STORAGE_KEY);
  } catch {
    return null;
  }
}

function writeStoredTaskId(taskId: string | null): void {
  try {
    if (taskId) {
      window.sessionStorage.setItem(LAST_ROLLOUT_TASK_STORAGE_KEY, taskId);
    } else {
      window.sessionStorage.removeItem(LAST_ROLLOUT_TASK_STORAGE_KEY);
    }
  } catch {
    // Ignore sessionStorage restrictions in private/sandboxed contexts.
  }
}

function RolloutBanner({
  taskId,
  onDismiss,
}: {
  taskId: string;
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showFailed, setShowFailed] = useState(false);
  const [failedPage, setFailedPage] = useState(1);

  const {
    data: detail,
    error: detailError,
    isError: isDetailError,
    refetch: refetchDetail,
  } = useQuery({
    queryKey: ['model-platform', 'rollout', taskId, failedPage],
    queryFn: () =>
      getRolloutTaskDetail(taskId, failedPage, FAILED_ITEMS_PAGE_SIZE),
    retry: false,
    refetchInterval: (query) =>
      query.state.data?.progress.status === 'running' ? 3000 : false,
  });

  const retry = useMutation({
    mutationFn: () => retryFailedTenants(taskId),
    onSuccess: () => {
      setFailedPage(1);
      queryClient.invalidateQueries({
        queryKey: ['model-platform', 'rollout', taskId],
      });
    },
    onError: (error: Error) => message.error(error.message),
  });

  const failedTotalPages = detail
    ? Math.max(1, Math.ceil(detail.failed_items.total / FAILED_ITEMS_PAGE_SIZE))
    : 1;

  useEffect(() => {
    if (detail && failedPage > failedTotalPages) {
      setFailedPage(failedTotalPages);
    }
  }, [detail, failedPage, failedTotalPages]);

  if (!detail) {
    if (!isDetailError) {
      return null;
    }
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive shadow-sm">
        <span>
          {t('modelPlatform.providers.rollout.loadFailed', {
            message: detailError instanceof Error ? detailError.message : '',
          })}
        </span>
        <div className="flex items-center gap-2">
          {failedPage > 1 && (
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setFailedPage(1)}
            >
              {t('modelPlatform.providers.rollout.backToFirstPage')}
            </Button>
          )}
          <Button size="sm" variant="secondary" onClick={() => refetchDetail()}>
            {t('modelPlatform.usage.error.retry')}
          </Button>
          <Button size="sm" variant="secondary" onClick={onDismiss}>
            {t('modelPlatform.providers.rollout.dismiss')}
          </Button>
        </div>
      </div>
    );
  }

  const { progress, failed_items: failedItems } = detail;

  return (
    <div className="space-y-3 rounded-xl border bg-card p-4 text-sm shadow-sm">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <span className="font-medium">{progress.model_name}</span>
          <span className="ml-2 text-muted-foreground">
            {progress.status === 'running'
              ? t('modelPlatform.providers.rollout.running')
              : t('modelPlatform.providers.rollout.finished')}
            :{' '}
            {t('modelPlatform.providers.rollout.progress', {
              succeeded: progress.succeeded,
              total: progress.total,
              pending: progress.pending,
              failed: progress.failed,
            })}
          </span>
          {progress.upgraded_users_count + progress.preserved_users_count >
            0 && (
            <span className="ml-2 text-muted-foreground">
              {t('modelPlatform.providers.rollout.usersSummary', {
                upgraded: progress.upgraded_users_count,
                preserved: progress.preserved_users_count,
              })}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {progress.failed > 0 && (
            <>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => setShowFailed((prev) => !prev)}
              >
                {showFailed ? (
                  <LucideChevronUp className="size-3.5" />
                ) : (
                  <LucideChevronDown className="size-3.5" />
                )}
                {showFailed
                  ? t('modelPlatform.providers.rollout.hideFailedDetails')
                  : t('modelPlatform.providers.rollout.showFailedDetails', {
                      count: progress.failed,
                    })}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                loading={retry.isPending}
                onClick={() => retry.mutate()}
              >
                {t('modelPlatform.providers.actions.retryFailed')}
              </Button>
            </>
          )}
          {progress.status !== 'running' && (
            <Button size="sm" variant="secondary" onClick={onDismiss}>
              {t('modelPlatform.providers.rollout.dismiss')}
            </Button>
          )}
        </div>
      </div>

      {showFailed && failedItems.items.length > 0 && (
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  {t('modelPlatform.providers.rollout.failedColumns.tenant')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.rollout.failedColumns.retries')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.rollout.failedColumns.error')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {failedItems.items.map((item) => (
                <TableRow key={item.tenant_id}>
                  <TableCell className="font-mono text-xs">
                    {item.tenant_id}
                  </TableCell>
                  <TableCell>{item.retry_count}</TableCell>
                  <TableCell className="max-w-md break-words font-mono text-xs text-destructive">
                    {item.last_error || '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {failedTotalPages > 1 && (
            <div className="flex items-center justify-between border-t px-4 py-2 text-xs">
              <span className="text-muted-foreground">
                {t('modelPlatform.usage.pagination.pageInfo', {
                  page: failedPage,
                  totalPages: failedTotalPages,
                })}
              </span>
              <div className="space-x-2">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={failedPage <= 1}
                  onClick={() => setFailedPage((p) => Math.max(1, p - 1))}
                >
                  {t('modelPlatform.usage.pagination.prev')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={failedPage >= failedTotalPages}
                  onClick={() =>
                    setFailedPage((p) => Math.min(failedTotalPages, p + 1))
                  }
                >
                  {t('modelPlatform.usage.pagination.next')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function OnboardDialog({
  open,
  onOpenChange,
  onStarted,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onStarted: (result: RolloutResult) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [modelType, setModelType] = useState<number>(
    MANAGED_MODEL_TYPES[0].bit,
  );
  const onboard = useMutation({
    mutationFn: () => onboardModel(name.trim(), modelType),
    onSuccess: (result) => {
      onStarted(result);
      setName('');
      onOpenChange(false);
    },
    onError: (error: Error) => message.error(error.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('modelPlatform.providers.onboardDialog.title')}
          </DialogTitle>
          <DialogDescription>
            {t('modelPlatform.providers.onboardDialog.description')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Input
            placeholder={t('modelPlatform.providers.onboardDialog.placeholder')}
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
          <div className="flex flex-wrap gap-2">
            {MANAGED_MODEL_TYPES.map((spec) => (
              <Button
                key={spec.bit}
                size="sm"
                variant={modelType === spec.bit ? 'default' : 'secondary'}
                onClick={() => setModelType(spec.bit)}
              >
                {t(`modelPlatform.modelTypes.${spec.key}`)}
              </Button>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t('modelPlatform.providers.onboardDialog.cancel')}
          </Button>
          <Button
            disabled={name.trim() === ''}
            loading={onboard.isPending}
            onClick={() => onboard.mutate()}
          >
            {t('modelPlatform.providers.onboardDialog.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function ModelProvidersPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [onboardOpen, setOnboardOpen] = useState(false);
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [taskId, setTaskId] = useState<string | null>(() => readStoredTaskId());

  const { data: models = [], isLoading } = useQuery({
    queryKey: ['model-platform', 'models'],
    queryFn: listManagedModels,
  });

  const {
    data: health,
    isFetching: healthFetching,
    isError: isHealthError,
    error: healthError,
    refetch: refetchHealth,
  } = useQuery({
    queryKey: ['model-platform', 'health'],
    queryFn: getAdminHealth,
    retry: false,
    refetchInterval: 30000,
  });

  const poolSummaries = useMemo(() => {
    return MANAGED_MODEL_TYPES.map((spec) => {
      const ofType = models.filter((m) => m.model_type === spec.bit);
      const activeCount = ofType.filter((m) => m.status === 'active').length;
      const defaultModel = ofType.find(
        (m) => m.is_default && m.status === 'active',
      );
      return {
        spec,
        totalCount: ofType.length,
        activeCount,
        defaultModelName: defaultModel?.model_name,
      };
    });
  }, [models]);

  const modelTypeLabel = (modelType: number) => {
    const spec = getModelTypeSpec(modelType);
    return spec
      ? t(`modelPlatform.modelTypes.${spec.key}`)
      : t('modelPlatform.modelTypes.unknown');
  };

  const renderReferences = (model: ManagedModel) => {
    const spec = getModelTypeSpec(model.model_type);
    if (spec?.refKind === 'dataset') {
      return t('modelPlatform.providers.references.datasets', {
        count: model.active_kb_count,
      });
    }
    if (spec?.refKind === 'assistant') {
      return t('modelPlatform.providers.references.assistants', {
        count: model.active_dialog_count,
      });
    }
    return t('modelPlatform.providers.references.none');
  };

  const describeAction = (action: Action, model: ManagedModel): string => {
    if (action === 'default') {
      return t('modelPlatform.providers.confirmDialog.describeDefault', {
        type: modelTypeLabel(model.model_type),
        model: model.model_name,
      });
    }
    if (action === 'reactivate') {
      return t('modelPlatform.providers.confirmDialog.describeReactivate', {
        model: model.model_name,
      });
    }
    const spec = getModelTypeSpec(model.model_type);
    if (spec?.refKind === 'dataset') {
      return t(
        'modelPlatform.providers.confirmDialog.describeRetireEmbedding',
        {
          count: model.active_kb_count,
          model: model.model_name,
        },
      );
    }
    if (spec?.refKind === 'assistant') {
      return t(
        'modelPlatform.providers.confirmDialog.describeRetireAssistant',
        {
          count: model.active_dialog_count,
          model: model.model_name,
        },
      );
    }
    return t('modelPlatform.providers.confirmDialog.describeRetireGeneric', {
      model: model.model_name,
    });
  };

  const startRollout = (result: RolloutResult) => {
    setTaskId(result.task_id);
    writeStoredTaskId(result.task_id);
    queryClient.invalidateQueries({ queryKey: ['model-platform', 'models'] });
  };

  const dismissRollout = () => {
    setTaskId(null);
    writeStoredTaskId(null);
  };

  const run = useMutation({
    mutationFn: ({ action, model }: PendingAction) =>
      action === 'retire'
        ? retireModel(model.id)
        : action === 'reactivate'
          ? reactivateModel(model.id)
          : setDefaultModel(model.id),
    onSuccess: (result) => {
      startRollout(result);
      setPending(null);
    },
    onError: (error: Error) => message.error(error.message),
  });

  const gatewayBadge = useMemo(() => {
    if (isHealthError) {
      return {
        variant: 'destructive' as const,
        label: t('modelPlatform.status.gatewayUnreachable'),
      };
    }
    if (!health) {
      return {
        variant: 'secondary' as const,
        label: t('modelPlatform.status.gatewayUnconfigured'),
      };
    }
    if (health.status === 'ok' && health.gateway === 'healthy') {
      return {
        variant: 'success' as const,
        label:
          health.latency_ms !== undefined
            ? t('modelPlatform.status.gatewayHealthyWithLatency', {
                latency: health.latency_ms,
              })
            : t('modelPlatform.status.gatewayHealthy'),
      };
    }
    if (health.status === 'degraded') {
      return {
        variant: 'destructive' as const,
        label: t('modelPlatform.status.gatewayDegraded'),
      };
    }
    return {
      variant: 'secondary' as const,
      label: t('modelPlatform.status.gatewayUnconfigured'),
    };
  }, [health, isHealthError, t]);

  const gatewayErrorMessage =
    isHealthError && healthError instanceof Error
      ? healthError.message
      : health?.status === 'degraded'
        ? health.error
        : undefined;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-7xl space-y-6 p-8">
        <div className="flex flex-wrap items-center justify-between gap-4 border-b pb-4">
          <div>
            <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
              <LucideBox className="size-6" />
              {t('modelPlatform.providers.title')}
            </h1>
            <p className="mt-1 text-sm text-muted-foreground">
              {t('modelPlatform.providers.subtitle')}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Badge
              variant={gatewayBadge.variant}
              className="gap-1.5"
              title={gatewayErrorMessage}
            >
              <LucideActivity className="size-3.5" />
              {gatewayBadge.label}
            </Badge>
            <Button
              size="sm"
              variant="secondary"
              loading={healthFetching}
              aria-label={t('modelPlatform.status.refreshHealth')}
              title={t('modelPlatform.status.refreshHealth')}
              onClick={() => refetchHealth()}
            >
              <LucideRefreshCw className="size-3.5" />
            </Button>
            <Button onClick={() => setOnboardOpen(true)}>
              <LucidePlus />
              {t('modelPlatform.providers.onboardButton')}
            </Button>
          </div>
        </div>

        {gatewayErrorMessage && (
          <div className="flex items-start gap-3 rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            <LucideAlertTriangle className="mt-0.5 size-4 shrink-0" />
            <div className="space-y-0.5">
              <div className="font-medium">
                {t('modelPlatform.status.gatewayErrorBannerTitle')}
              </div>
              <div className="break-all font-mono text-xs">
                {gatewayErrorMessage}
              </div>
            </div>
          </div>
        )}

        <section className="space-y-3">
          <h2 className="text-sm font-semibold text-muted-foreground">
            {t('modelPlatform.providers.poolOverviewTitle')}
          </h2>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {poolSummaries.map(({ spec, activeCount, defaultModelName }) => (
              <div
                key={spec.bit}
                className="rounded-xl border bg-card p-4 text-card-foreground shadow-sm"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium">
                    {t(`modelPlatform.modelTypes.${spec.key}`)}
                  </span>
                  <Badge variant={activeCount > 0 ? 'success' : 'secondary'}>
                    {t('modelPlatform.providers.poolActiveCount', {
                      count: activeCount,
                    })}
                  </Badge>
                </div>
                <p
                  className="mt-2 truncate text-xs text-muted-foreground"
                  title={defaultModelName}
                >
                  {defaultModelName
                    ? t('modelPlatform.providers.poolDefaultLabel', {
                        model: defaultModelName,
                      })
                    : t('modelPlatform.providers.poolNoDefault')}
                </p>
              </div>
            ))}
          </div>
        </section>

        {taskId && (
          <RolloutBanner
            key={taskId}
            taskId={taskId}
            onDismiss={dismissRollout}
          />
        )}

        <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  {t('modelPlatform.providers.columns.model')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.columns.type')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.columns.status')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.columns.tenants')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.providers.columns.references')}
                </TableHead>
                <TableHead className="text-right">
                  {t('modelPlatform.providers.columns.actions')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {models.map((model) => (
                <TableRow key={model.id}>
                  <TableCell className="font-medium">
                    <span className="flex items-center gap-2">
                      {model.model_name}
                      {model.is_default && (
                        <LucideStar className="size-4 text-amber-500" />
                      )}
                    </span>
                  </TableCell>
                  <TableCell>{modelTypeLabel(model.model_type)}</TableCell>
                  <TableCell>
                    <Badge
                      variant={
                        model.status === 'active' ? 'success' : 'secondary'
                      }
                    >
                      {model.status === 'active'
                        ? t('modelPlatform.status.active')
                        : t('modelPlatform.status.retired')}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    {model.tenants.active} / {model.tenants.deprecated} /{' '}
                    {model.tenants.tombstoned}
                  </TableCell>
                  <TableCell>{renderReferences(model)}</TableCell>
                  <TableCell className="space-x-2 text-right">
                    {model.status === 'retired' ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() =>
                          setPending({ action: 'reactivate', model })
                        }
                      >
                        {t('modelPlatform.providers.actions.reactivate')}
                      </Button>
                    ) : (
                      <>
                        {!model.is_default && (
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() =>
                              setPending({ action: 'default', model })
                            }
                          >
                            {t('modelPlatform.providers.actions.setDefault')}
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="destructive"
                          disabled={model.is_default}
                          title={
                            model.is_default
                              ? t(
                                  'modelPlatform.providers.actions.retireDisabledTitle',
                                )
                              : undefined
                          }
                          onClick={() =>
                            setPending({ action: 'retire', model })
                          }
                        >
                          {t('modelPlatform.providers.actions.retire')}
                        </Button>
                      </>
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {!isLoading && models.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="py-8 text-center text-muted-foreground"
                  >
                    {t('modelPlatform.providers.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>

        <OnboardDialog
          open={onboardOpen}
          onOpenChange={setOnboardOpen}
          onStarted={startRollout}
        />

        <Dialog open={pending !== null} onOpenChange={() => setPending(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {pending?.action === 'retire' &&
                  t('modelPlatform.providers.confirmDialog.retireTitle')}
                {pending?.action === 'reactivate' &&
                  t('modelPlatform.providers.confirmDialog.reactivateTitle')}
                {pending?.action === 'default' &&
                  t('modelPlatform.providers.confirmDialog.defaultTitle')}
              </DialogTitle>
              <DialogDescription>
                {pending && describeAction(pending.action, pending.model)}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="secondary" onClick={() => setPending(null)}>
                {t('modelPlatform.providers.confirmDialog.cancel')}
              </Button>
              <Button
                variant={
                  pending?.action === 'retire' ? 'destructive' : 'default'
                }
                loading={run.isPending}
                onClick={() => pending && run.mutate(pending)}
              >
                {t('modelPlatform.providers.confirmDialog.confirm')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    </div>
  );
}
