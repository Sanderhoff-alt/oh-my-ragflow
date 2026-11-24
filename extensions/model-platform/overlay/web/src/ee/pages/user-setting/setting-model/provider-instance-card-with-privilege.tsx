import { ShieldCheck } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { IProviderInstance } from '@/interfaces/database/llm';
import { ProviderInstanceCard } from '@/pages/user-setting/setting-model/instance-card/provider-instance-card';

type ManagedInstanceMetadata = IProviderInstance & {
  is_managed?: boolean | string;
  api_key_config?: {
    is_managed?: boolean | string;
  };
};

type ProviderInstanceCardWithPrivilegeProps = {
  providerName: string;
  instance: IProviderInstance;
  defaultOpen: boolean;
};

function isFlagEnabled(value: boolean | string | undefined): boolean {
  return value === true || value === 'true';
}

/**
 * Enterprise seam around a provider instance card.
 * Detects platform-managed instances (covering Chat, Embedding, Rerank, ASR,
 * Vision, TTS, and Decision models routed through the managed platform
 * instance) and renders a localized read-only protection badge while hiding
 * mutation and deletion controls.
 */
export function ProviderInstanceCardWithPrivilege({
  providerName,
  instance,
  defaultOpen,
}: ProviderInstanceCardWithPrivilegeProps) {
  const { t } = useTranslation();

  const isManaged = useMemo(() => {
    const meta = instance as ManagedInstanceMetadata | undefined;
    return (
      meta?.instance_name === 'platform' ||
      isFlagEnabled(meta?.is_managed) ||
      isFlagEnabled(meta?.api_key_config?.is_managed)
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
    <div className="group relative rounded-lg border border-blue-500/30 bg-blue-500/5 p-0.5">
      <div className="pointer-events-none absolute top-2 right-2 z-10 flex items-center gap-1.5 rounded-full border border-blue-300 bg-blue-100 px-2.5 py-0.5 text-xs font-medium text-blue-800 shadow-sm dark:border-blue-800 dark:bg-blue-950/80 dark:text-blue-300">
        <ShieldCheck className="size-3.5 text-blue-600 dark:text-blue-400" />
        <span>{t('modelPlatform.badge.managed')}</span>
      </div>
      <div className="pointer-events-auto [&_[data-testid=draft-delete]]:hidden [&_[data-testid=instance-delete]]:hidden [&_[data-testid=instance-save]]:hidden [&_button:has(.lucide-trash-2)]:hidden [&_input]:pointer-events-none [&_input]:opacity-85">
        <ProviderInstanceCard
          providerName={providerName}
          instance={instance}
          defaultOpen={defaultOpen}
        />
      </div>
    </div>
  );
}
