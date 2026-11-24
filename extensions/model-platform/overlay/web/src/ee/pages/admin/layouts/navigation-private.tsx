import { LucideBarChart3, LucideBox } from 'lucide-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PrivateRoutes } from '@/ee/private-routes';

export type AdminNavItem = {
  path: string;
  name: string;
  icon: ReactNode;
};

export type AdminNavExtension = {
  items: AdminNavItem[];
  hiddenPaths: string[];
};

/**
 * Enterprise navigation extension for Model Platform.
 * Appends Model Providers and Model Usage Statistics to the admin navigation sidebar.
 */
export function useEnterpriseAdminNav(): AdminNavExtension {
  const { t } = useTranslation();

  return {
    items: [
      {
        path: PrivateRoutes.AdminModelProviders,
        name: t('modelPlatform.nav.modelProviders'),
        icon: <LucideBox className="size-[1em]" />,
      },
      {
        path: PrivateRoutes.AdminModelUsageStatistics,
        name: t('modelPlatform.nav.modelUsage'),
        icon: <LucideBarChart3 className="size-[1em]" />,
      },
    ],
    hiddenPaths: [],
  };
}
