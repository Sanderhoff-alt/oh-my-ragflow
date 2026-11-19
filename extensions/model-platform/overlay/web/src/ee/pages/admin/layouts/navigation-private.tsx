import type { ReactNode } from 'react';
import { LucideBox, LucideBarChart3 } from 'lucide-react';
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
  return {
    items: [
      {
        path: PrivateRoutes.AdminModelProviders,
        name: 'Model Providers',
        icon: <LucideBox className="size-[1em]" />,
      },
      {
        path: PrivateRoutes.AdminModelUsageStatistics,
        name: 'Model Usage',
        icon: <LucideBarChart3 className="size-[1em]" />,
      },
    ],
    hiddenPaths: [],
  };
}
