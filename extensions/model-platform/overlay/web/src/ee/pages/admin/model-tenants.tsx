import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  LucideAlertTriangle,
  LucideCheckCircle2,
  LucideKeyRound,
  LucideRefreshCw,
  LucideShieldAlert,
  LucideShieldCheck,
} from 'lucide-react';
import { useRef, useState } from 'react';
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
  auditTenantUsage,
  listTenantKeys,
  rotateTenantKey,
  type AuditIsolationReport,
  type KeyRotationResult,
  type TenantKeyItem,
  type TenantReconcileStatus,
} from '@/ee/utils/model-platform-client';

const PAGE_SIZE = 20;

const TENANT_STATUSES: readonly TenantReconcileStatus[] = [
  'ready',
  'degraded',
  'failed',
  'pending',
  'provisioning',
  'deleted',
] as const;

function formatTimestamp(iso?: string): string {
  if (!iso) {
    return '—';
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return iso;
  }
  return date.toLocaleString();
}

function formatSpend(value: number): string {
  return `$${value.toFixed(6)}`;
}

function statusBadgeVariant(
  status: TenantReconcileStatus,
): 'success' | 'destructive' | 'secondary' {
  if (status === 'ready') {
    return 'success';
  }
  if (status === 'failed' || status === 'degraded') {
    return 'destructive';
  }
  return 'secondary';
}

export default function ModelTenantsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const [status, setStatus] = useState<TenantReconcileStatus>('ready');
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  const currentAfter = cursorStack[cursorStack.length - 1];

  const [rotateTarget, setRotateTarget] = useState<TenantKeyItem | null>(null);
  const [lastRotation, setLastRotation] = useState<KeyRotationResult | null>(
    null,
  );
  const [auditTarget, setAuditTarget] = useState<string | null>(null);
  const [auditReport, setAuditReport] = useState<AuditIsolationReport | null>(
    null,
  );
  const [auditError, setAuditError] = useState<string | null>(null);
  const auditGenerationRef = useRef(0);

  const {
    data: page,
    isLoading,
    isFetching,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['model-platform', 'tenants', status, currentAfter],
    queryFn: () =>
      listTenantKeys({
        status,
        after: currentAfter,
        page_size: PAGE_SIZE,
      }),
  });

  const items = page?.items ?? [];
  const total = page?.total ?? 0;
  const pageNumber = cursorStack.length + 1;
  const hasNextPage = items.length === PAGE_SIZE;

  const rotateMutation = useMutation({
    mutationFn: (tenantId: string) => rotateTenantKey(tenantId),
    onSuccess: (result) => {
      setLastRotation(result);
      setRotateTarget(null);
      if (result.old_keys_revoked) {
        message.success(
          t('modelPlatform.actions.rotateKeySuccess', {
            tenantId: result.tenant_id,
            alias: result.key_alias,
          }),
        );
      } else {
        message.warning(
          t('modelPlatform.actions.rotateKeyPendingRevocation', {
            tenantId: result.tenant_id,
          }),
        );
      }
      queryClient.invalidateQueries({
        queryKey: ['model-platform', 'tenants'],
      });
    },
    onError: (err: Error) => {
      message.error(err.message);
      setRotateTarget(null);
      queryClient.invalidateQueries({
        queryKey: ['model-platform', 'tenants'],
      });
    },
  });

  const auditMutation = useMutation({
    mutationFn: ({ tenantId }: { tenantId: string; generation: number }) =>
      auditTenantUsage(tenantId),
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
      setAuditReport(null);
      setAuditError(err.message);
    },
  });

  const handleStatusChange = (nextStatus: TenantReconcileStatus) => {
    setStatus(nextStatus);
    setCursorStack([]);
  };

  const handleNextPage = () => {
    const lastItem = items[items.length - 1];
    if (!lastItem) {
      return;
    }
    setCursorStack((prev) => [...prev, lastItem.tenant_id]);
  };

  const handlePrevPage = () => {
    setCursorStack((prev) => prev.slice(0, -1));
  };

  const runAuditForTenant = (tenantId: string) => {
    const generation = ++auditGenerationRef.current;
    setAuditReport(null);
    setAuditError(null);
    auditMutation.mutate({ tenantId, generation });
  };

  const openAuditDialog = (tenantId: string) => {
    setAuditTarget(tenantId);
    runAuditForTenant(tenantId);
  };

  const closeAuditDialog = () => {
    auditGenerationRef.current += 1;
    setAuditTarget(null);
    setAuditReport(null);
    setAuditError(null);
  };

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-7xl space-y-6 p-8">
        <div className="flex items-center justify-between border-b pb-4">
          <div>
            <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
              <LucideKeyRound className="size-6" />
              {t('modelPlatform.tenants.title')}
            </h1>
            <p className="mt-1 text-sm text-muted-foreground">
              {t('modelPlatform.tenants.subtitle')}
            </p>
          </div>
          <Button
            variant="secondary"
            size="sm"
            loading={isFetching}
            onClick={() => refetch()}
          >
            <LucideRefreshCw className="size-3.5" />
            {t('modelPlatform.tenants.refresh')}
          </Button>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card p-4 shadow-sm">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              {t('modelPlatform.tenants.filters.status')}:
            </span>
            {TENANT_STATUSES.map((st) => (
              <Button
                key={st}
                size="sm"
                variant={status === st ? 'default' : 'secondary'}
                onClick={() => handleStatusChange(st)}
              >
                {t(`modelPlatform.tenants.statuses.${st}`)}
              </Button>
            ))}
          </div>
          <Badge variant="secondary">
            {t('modelPlatform.tenants.totalCount', { count: total })}
          </Badge>
        </div>

        {lastRotation && (
          <div className="flex items-start justify-between gap-4 rounded-xl border bg-card p-4 text-sm shadow-sm">
            <div className="flex items-start gap-3">
              {lastRotation.old_keys_revoked ? (
                <LucideCheckCircle2 className="mt-0.5 size-5 shrink-0 text-emerald-500" />
              ) : (
                <LucideAlertTriangle className="mt-0.5 size-5 shrink-0 text-amber-500" />
              )}
              <div className="space-y-1">
                <div className="font-medium">
                  {lastRotation.old_keys_revoked
                    ? t('modelPlatform.actions.rotateKeyResultConverged', {
                        tenantId: lastRotation.tenant_id,
                      })
                    : t('modelPlatform.actions.rotateKeyResultPending', {
                        tenantId: lastRotation.tenant_id,
                      })}
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                  <span>
                    {t('modelPlatform.actions.rotateKeyAliasLabel', {
                      alias: lastRotation.key_alias,
                    })}
                  </span>
                  <span className="font-mono">
                    {t('modelPlatform.actions.rotateKeyFingerprintLabel', {
                      fingerprint: lastRotation.new_key_fingerprint,
                    })}
                  </span>
                  <span>
                    {t('modelPlatform.actions.rotateKeyTimestampLabel', {
                      timestamp: formatTimestamp(lastRotation.rotated_at),
                    })}
                  </span>
                  <span>
                    {t('modelPlatform.actions.rotateKeyRevokedCount', {
                      count: lastRotation.revoked_keys,
                    })}
                  </span>
                </div>
              </div>
            </div>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setLastRotation(null)}
            >
              {t('modelPlatform.tenants.dismiss')}
            </Button>
          </div>
        )}

        <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  {t('modelPlatform.tenants.columns.tenant')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.tenants.columns.status')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.tenants.columns.keyAlias')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.tenants.columns.fingerprint')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.tenants.columns.lastRotated')}
                </TableHead>
                <TableHead>
                  {t('modelPlatform.tenants.columns.convergence')}
                </TableHead>
                <TableHead className="text-right">
                  {t('modelPlatform.tenants.columns.actions')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => {
                const isDirty = Boolean(item.key_dirty_since);
                const canRotate = item.status === 'ready';
                return (
                  <TableRow key={item.tenant_id}>
                    <TableCell className="font-mono text-xs">
                      {item.tenant_id}
                    </TableCell>
                    <TableCell>
                      <Badge variant={statusBadgeVariant(item.status)}>
                        {t(`modelPlatform.tenants.statuses.${item.status}`)}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {item.key_alias || '—'}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {item.key_fingerprint || '—'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      <div>{formatTimestamp(item.key_rotated_at)}</div>
                      {item.key_rotated_by && (
                        <div className="font-mono">{item.key_rotated_by}</div>
                      )}
                    </TableCell>
                    <TableCell>
                      {isDirty ? (
                        <Badge
                          variant="destructive"
                          title={formatTimestamp(item.key_dirty_since)}
                        >
                          {t('modelPlatform.tenants.convergence.dirty')}
                        </Badge>
                      ) : (
                        <Badge variant="success">
                          {t('modelPlatform.tenants.convergence.clean')}
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell className="space-x-2 text-right">
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => openAuditDialog(item.tenant_id)}
                      >
                        {t('modelPlatform.audit.runAction')}
                      </Button>
                      <Button
                        size="sm"
                        variant={isDirty ? 'default' : 'destructive'}
                        disabled={!canRotate}
                        title={
                          canRotate
                            ? undefined
                            : t('modelPlatform.actions.rotateKeyDisabledTitle')
                        }
                        onClick={() => setRotateTarget(item)}
                      >
                        {isDirty
                          ? t('modelPlatform.actions.rotateKeyResume')
                          : t('modelPlatform.actions.rotateKey')}
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
              {isError ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-8 text-center">
                    <div className="flex flex-col items-center justify-center gap-3 text-sm text-destructive">
                      <span>
                        {t('modelPlatform.tenants.loadFailed', {
                          message: error instanceof Error ? error.message : '',
                        })}
                      </span>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => refetch()}
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
                      {t('modelPlatform.tenants.empty')}
                    </TableCell>
                  </TableRow>
                )
              )}
            </TableBody>
          </Table>

          {(cursorStack.length > 0 || hasNextPage) && (
            <div className="flex items-center justify-between border-t px-6 py-3 text-sm">
              <span className="text-muted-foreground">
                {t('modelPlatform.tenants.pageLabel', { page: pageNumber })}
              </span>
              <div className="space-x-2">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={cursorStack.length === 0}
                  onClick={handlePrevPage}
                >
                  {t('modelPlatform.usage.pagination.prev')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={!hasNextPage}
                  onClick={handleNextPage}
                >
                  {t('modelPlatform.usage.pagination.next')}
                </Button>
              </div>
            </div>
          )}
        </div>

        <Dialog
          open={rotateTarget !== null}
          onOpenChange={(open) => {
            if (!open && !rotateMutation.isPending) {
              setRotateTarget(null);
            }
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {rotateTarget?.key_dirty_since
                  ? t('modelPlatform.actions.rotateKeyResumeTitle')
                  : t('modelPlatform.actions.rotateKeyTitle')}
              </DialogTitle>
              <DialogDescription>
                {rotateTarget &&
                  (rotateTarget.key_dirty_since
                    ? t('modelPlatform.actions.rotateKeyResumeDescription', {
                        tenantId: rotateTarget.tenant_id,
                      })
                    : t('modelPlatform.actions.rotateKeyDescription', {
                        tenantId: rotateTarget.tenant_id,
                      }))}
              </DialogDescription>
            </DialogHeader>
            {rotateTarget && (
              <div className="space-y-1 rounded-lg border bg-muted/40 p-3 text-xs">
                <div>
                  <span className="text-muted-foreground">
                    {t('modelPlatform.tenants.columns.keyAlias')}:{' '}
                  </span>
                  <span className="font-mono">
                    {rotateTarget.key_alias || '—'}
                  </span>
                </div>
                <div>
                  <span className="text-muted-foreground">
                    {t('modelPlatform.tenants.columns.fingerprint')}:{' '}
                  </span>
                  <span className="font-mono">
                    {rotateTarget.key_fingerprint || '—'}
                  </span>
                </div>
              </div>
            )}
            <DialogFooter>
              <Button
                variant="secondary"
                disabled={rotateMutation.isPending}
                onClick={() => setRotateTarget(null)}
              >
                {t('modelPlatform.actions.rotateKeyCancel')}
              </Button>
              <Button
                variant="destructive"
                loading={rotateMutation.isPending}
                onClick={() =>
                  rotateTarget && rotateMutation.mutate(rotateTarget.tenant_id)
                }
              >
                {t('modelPlatform.actions.rotateKeyConfirm')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <Dialog
          open={auditTarget !== null}
          onOpenChange={(open) => {
            if (!open) {
              closeAuditDialog();
            }
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                {auditReport ? (
                  auditReport.audit_passed ? (
                    <LucideShieldCheck className="size-5 text-emerald-500" />
                  ) : (
                    <LucideShieldAlert className="size-5 text-destructive" />
                  )
                ) : (
                  <LucideShieldCheck className="size-5" />
                )}
                {t('modelPlatform.audit.dialogTitle')}
              </DialogTitle>
              <DialogDescription>
                {t('modelPlatform.audit.dialogSubtitle', {
                  tenantId: auditTarget ?? '',
                })}
              </DialogDescription>
            </DialogHeader>

            {auditMutation.isPending && (
              <div className="py-6 text-center text-sm text-muted-foreground">
                {t('modelPlatform.audit.running')}
              </div>
            )}

            {auditError && (
              <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
                {t('modelPlatform.audit.failed', { message: auditError })}
              </div>
            )}

            {auditReport && (
              <div className="space-y-4 text-sm">
                <div className="flex items-center justify-between rounded-lg border p-3">
                  <span className="font-medium">
                    {t('modelPlatform.audit.statusLabel')}
                  </span>
                  <Badge
                    variant={
                      auditReport.audit_passed ? 'success' : 'destructive'
                    }
                  >
                    {auditReport.audit_passed
                      ? t('modelPlatform.audit.passed')
                      : t('modelPlatform.audit.violationDetected')}
                  </Badge>
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="rounded-lg border p-3">
                    <div className="text-xs text-muted-foreground">
                      {t('modelPlatform.audit.managedSpend')}
                    </div>
                    <div className="mt-1 font-mono text-base font-semibold">
                      {formatSpend(auditReport.managed_spend_total)}
                    </div>
                  </div>
                  <div className="rounded-lg border p-3">
                    <div className="text-xs text-muted-foreground">
                      {t('modelPlatform.audit.selfOwnedSpend')}
                    </div>
                    <div className="mt-1 font-mono text-base font-semibold">
                      {formatSpend(auditReport.self_owned_spend_total)}
                    </div>
                  </div>
                </div>

                {auditReport.unmanaged_models_detected &&
                  auditReport.unmanaged_models_detected.length > 0 && (
                    <div className="space-y-1.5">
                      <div className="text-xs font-medium text-destructive">
                        {t('modelPlatform.audit.unmanagedModels')}
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        {auditReport.unmanaged_models_detected.map((model) => (
                          <Badge key={model} variant="destructive">
                            {model}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  )}

                {auditReport.violations &&
                  auditReport.violations.length > 0 && (
                    <div className="space-y-1.5">
                      <div className="text-xs font-medium text-destructive">
                        {t('modelPlatform.audit.violations')}
                      </div>
                      <ul className="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-destructive/30 bg-destructive/5 p-3 font-mono text-xs text-destructive">
                        {auditReport.violations.map((v) => (
                          <li key={v}>{v}</li>
                        ))}
                      </ul>
                    </div>
                  )}
              </div>
            )}

            <DialogFooter>
              {auditTarget && (
                <Button
                  variant="secondary"
                  loading={auditMutation.isPending}
                  onClick={() => runAuditForTenant(auditTarget)}
                >
                  {t('modelPlatform.audit.rerun')}
                </Button>
              )}
              <Button onClick={closeAuditDialog}>
                {t('modelPlatform.audit.close')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    </div>
  );
}
