import type { ComponentType, ReactNode } from 'react';

/**
 * Enterprise seam for private routes.
 * Registers model-platform admin routes while keeping upstream routes untouched.
 */
export enum PrivateRoutes {
  Price = '/price',
  Billing = '/billing',
  EvaluationDetail = '/evaluation-detail',
  Evaluation = '/evaluation',
  Tickets = '/support',
  TicketCreate = '/support/create',
  TicketDetail = '/support/:id',
  AdminRoleConfigModelProviders = '/admin/roles/:roleName/model-providers',
  AdminModelUsageStatistics = '/admin/model-usage-statistics',
  AdminModelTenants = '/admin/model-tenants',
  AdminSettings = '/admin/settings',
  AdminSSOProviders = '/admin/sso',
  AdminModelProviders = '/admin/model-providers',
}

export type PrivateRoute = {
  path?: string;
  Component?: () => Promise<{ default: ComponentType<any> }>;
  errorElement?: ReactNode;
  children?: PrivateRoute[];
  replace?: string[];
  matchIndex?: number;
};

export const privateRoutes: PrivateRoute[] = [
  {
    path: PrivateRoutes.AdminModelProviders,
    Component: () => import('@/ee/pages/admin/model-providers'),
  },
  {
    path: PrivateRoutes.AdminModelUsageStatistics,
    Component: () => import('@/ee/pages/admin/model-usage-statistics'),
  },
  {
    path: PrivateRoutes.AdminModelTenants,
    Component: () => import('@/ee/pages/admin/model-tenants'),
  },
];
