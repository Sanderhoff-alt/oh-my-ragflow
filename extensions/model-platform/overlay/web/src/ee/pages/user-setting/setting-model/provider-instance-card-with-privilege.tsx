import { useMemo } from 'react';
import { ShieldCheck } from 'lucide-react';
import { IProviderInstance } from '@/interfaces/database/llm';
import { ProviderInstanceCard } from '@/pages/user-setting/setting-model/instance-card/provider-instance-card';

type ProviderInstanceCardWithPrivilegeProps = {
  providerName: string;
  instance: IProviderInstance;
  defaultOpen: boolean;
};

/**
 * Enterprise seam around a provider instance card.
 * Detects platform-managed instances and decorates them with a protection badge
 * while disabling modification and deletion controls in the UI.
 */
export function ProviderInstanceCardWithPrivilege({
  providerName,
  instance,
  defaultOpen,
}: ProviderInstanceCardWithPrivilegeProps) {
  const isManaged = useMemo(() => {
    return (
      instance?.instance_name === 'platform' ||
      (instance as any)?.is_managed === true ||
      (instance as any)?.is_managed === 'true' ||
      (instance as any)?.api_key_config?.is_managed === true ||
      (instance as any)?.api_key_config?.is_managed === 'true'
    );
  }, [instance]);

  if (!isManaged) {
    return (
      <ProviderInstanceCard
        providerName={providerName}
        instance={instance}
        defaultOpen={defaultOpen}
      />
    );
  }

  return (
    <div className="relative group border border-blue-500/30 rounded-lg p-0.5 bg-blue-500/5">
      <div className="absolute top-2 right-2 z-10 flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium bg-blue-100 text-blue-800 dark:bg-blue-950/80 dark:text-blue-300 border border-blue-300 dark:border-blue-800 shadow-sm pointer-events-none">
        <ShieldCheck className="size-3.5 text-blue-600 dark:text-blue-400" />
        <span>平台托管 (Platform Managed)</span>
      </div>
      <div className="[&_[data-testid=instance-delete]]:hidden [&_[data-testid=draft-delete]]:hidden [&_[data-testid=instance-save]]:hidden [&_button:has(.lucide-trash-2)]:hidden [&_input]:pointer-events-none [&_input]:opacity-85 pointer-events-auto">
        <ProviderInstanceCard
          providerName={providerName}
          instance={instance}
          defaultOpen={defaultOpen}
        />
      </div>
    </div>
  );
}
