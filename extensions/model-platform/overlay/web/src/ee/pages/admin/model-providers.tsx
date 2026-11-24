import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { LucideBox, LucidePlus, LucideStar } from 'lucide-react';
import { useState } from 'react';

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
  getRolloutProgress,
  listManagedModels,
  onboardModel,
  reactivateModel,
  retireModel,
  retryFailedTenants,
  setDefaultModel,
  type ManagedModel,
  type RolloutResult,
} from '@/ee/utils/model-platform-client';

const MODEL_TYPE_LABELS: Record<number, string> = {
  1: 'Chat',
  2: 'Embedding',
  4: 'Speech to text',
  8: 'Vision',
  16: 'Rerank',
};

const ONBOARD_TYPES = [1, 2, 16];

type Action = 'retire' | 'reactivate' | 'default';

type PendingAction = { action: Action; model: ManagedModel };

function describe(action: Action, model: ManagedModel): string {
  if (action === 'default') {
    return `Platform default users of ${MODEL_TYPE_LABELS[model.model_type]} will move to ${model.model_name}. Users who chose a model themselves keep it.`;
  }
  if (action === 'reactivate') {
    return `${model.model_name} will become available again in every tenant.`;
  }
  if (model.model_type === 2) {
    return `${model.active_kb_count} knowledge base(s) use ${model.model_name}. Tenants that still have knowledge bases keep it read-only, the others lose it at once. Users who default to it move to the platform default.`;
  }
  return `${model.model_name} will be disabled in every tenant at once. ${model.active_dialog_count} assistant(s) use it and will ask their owners to pick another model. Users who default to it move to the platform default.`;
}

function RolloutBanner({ taskId }: { taskId: string }) {
  const queryClient = useQueryClient();
  const { data: progress } = useQuery({
    queryKey: ['model-platform', 'rollout', taskId],
    queryFn: () => getRolloutProgress(taskId),
    refetchInterval: (query) =>
      query.state.data?.status === 'running' ? 3000 : false,
  });
  const retry = useMutation({
    mutationFn: () => retryFailedTenants(taskId),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: ['model-platform', 'rollout', taskId],
      }),
    onError: (error: Error) => message.error(error.message),
  });

  if (!progress) {
    return null;
  }
  return (
    <div className="flex items-center justify-between rounded-lg border bg-card p-4 text-sm">
      <div>
        <span className="font-medium">{progress.model_name}</span>
        <span className="ml-2 text-muted-foreground">
          {progress.status === 'running' ? 'Rolling out' : 'Rollout finished'}:{' '}
          {progress.succeeded}/{progress.total} tenants done, {progress.pending}{' '}
          pending, {progress.failed} failed
        </span>
        {progress.upgraded_users_count + progress.preserved_users_count > 0 && (
          <span className="ml-2 text-muted-foreground">
            ({progress.upgraded_users_count} users moved,{' '}
            {progress.preserved_users_count} kept their own choice)
          </span>
        )}
      </div>
      {progress.failed > 0 && (
        <Button
          size="sm"
          variant="secondary"
          loading={retry.isPending}
          onClick={() => retry.mutate()}
        >
          Retry failed tenants
        </Button>
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
  const [name, setName] = useState('');
  const [modelType, setModelType] = useState(1);
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
          <DialogTitle>Onboard a model</DialogTitle>
          <DialogDescription>
            The model must already be routed by the gateway. Onboarding makes it
            available to every Ready tenant and never changes a default.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Input
            placeholder="Gateway model name, for example deepseek-v3"
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
          <div className="flex gap-2">
            {ONBOARD_TYPES.map((type) => (
              <Button
                key={type}
                size="sm"
                variant={modelType === type ? 'default' : 'secondary'}
                onClick={() => setModelType(type)}
              >
                {MODEL_TYPE_LABELS[type]}
              </Button>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            disabled={name.trim() === ''}
            loading={onboard.isPending}
            onClick={() => onboard.mutate()}
          >
            Onboard
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function ModelProvidersPage() {
  const queryClient = useQueryClient();
  const [onboardOpen, setOnboardOpen] = useState(false);
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [taskId, setTaskId] = useState<string | null>(null);

  const { data: models = [], isLoading } = useQuery({
    queryKey: ['model-platform', 'models'],
    queryFn: listManagedModels,
  });

  const startRollout = (result: RolloutResult) => {
    setTaskId(result.task_id);
    queryClient.invalidateQueries({ queryKey: ['model-platform', 'models'] });
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

  return (
    <div className="mx-auto max-w-7xl space-y-6 p-8">
      <div className="flex items-center justify-between border-b pb-4">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
            <LucideBox className="size-6" />
            Platform Managed Models
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Models the platform provides to every tenant through the gateway.
          </p>
        </div>
        <Button onClick={() => setOnboardOpen(true)}>
          <LucidePlus />
          Onboard model
        </Button>
      </div>

      {taskId && <RolloutBanner taskId={taskId} />}

      <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Model</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Tenants (active / read-only / retired)</TableHead>
              <TableHead>References</TableHead>
              <TableHead className="text-right">Actions</TableHead>
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
                <TableCell>{MODEL_TYPE_LABELS[model.model_type]}</TableCell>
                <TableCell>
                  <Badge
                    variant={
                      model.status === 'active' ? 'success' : 'secondary'
                    }
                  >
                    {model.status}
                  </Badge>
                </TableCell>
                <TableCell>
                  {model.tenants.active} / {model.tenants.deprecated} /{' '}
                  {model.tenants.tombstoned}
                </TableCell>
                <TableCell>
                  {model.model_type === 2
                    ? `${model.active_kb_count} datasets`
                    : `${model.active_dialog_count} assistants`}
                </TableCell>
                <TableCell className="space-x-2 text-right">
                  {model.status === 'retired' ? (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() =>
                        setPending({ action: 'reactivate', model })
                      }
                    >
                      Reactivate
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
                          Set default
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="destructive"
                        disabled={model.is_default}
                        title={
                          model.is_default
                            ? 'Set another model as default before retiring this one'
                            : undefined
                        }
                        onClick={() => setPending({ action: 'retire', model })}
                      >
                        Retire
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
                  No managed models yet. Add a route to the gateway, then
                  onboard it here.
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
              {pending?.action === 'retire' && 'Retire model'}
              {pending?.action === 'reactivate' && 'Reactivate model'}
              {pending?.action === 'default' && 'Set as platform default'}
            </DialogTitle>
            <DialogDescription>
              {pending && describe(pending.action, pending.model)}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setPending(null)}>
              Cancel
            </Button>
            <Button
              variant={pending?.action === 'retire' ? 'destructive' : 'default'}
              loading={run.isPending}
              onClick={() => pending && run.mutate(pending)}
            >
              Confirm
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
