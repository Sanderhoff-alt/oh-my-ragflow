import { render, screen } from '@testing-library/react';
import type { RouteObject } from 'react-router';

import { injectPrivateRoutes } from '@/ee/merge-routes';
import { PrivateRoutes, type PrivateRoute } from '@/ee/private-routes';

function buildUpstreamAdminTree(): RouteObject[] {
  return [
    { path: '/login-next' },
    {
      path: '/admin',
      children: [
        { path: '/admin' },
        {
          path: '/admin',
          children: [
            { path: '/admin/users/:id' },
            {
              children: [
                { path: '/admin/services' },
                { path: '/admin/users' },
                { path: '/admin/sandbox-settings' },
              ],
            },
          ],
        },
      ],
    },
  ];
}

function getNavigationChildren(routes: RouteObject[]): RouteObject[] {
  const admin = routes.find((r) => r.path === '/admin');
  const authLayout = admin?.children?.[1];
  const navLayout = authLayout?.children?.[1];
  return navLayout?.children ?? [];
}

describe('injectPrivateRoutes', () => {
  it('injects all default model-platform admin routes into the admin navigation layout', () => {
    const routes = buildUpstreamAdminTree();

    const injected = injectPrivateRoutes(routes);
    const navChildren = getNavigationChildren(routes);
    const paths = navChildren.map((child) => child.path);

    expect(injected).toBe(true);
    expect(paths).toEqual([
      '/admin/services',
      '/admin/users',
      '/admin/sandbox-settings',
      PrivateRoutes.AdminModelProviders,
      PrivateRoutes.AdminModelUsageStatistics,
      PrivateRoutes.AdminModelTenants,
    ]);
  });

  it('is idempotent and does not duplicate routes when invoked repeatedly', () => {
    const routes = buildUpstreamAdminTree();

    injectPrivateRoutes(routes);
    injectPrivateRoutes(routes);

    const paths = getNavigationChildren(routes).map((child) => child.path);
    expect(
      paths.filter((p) => p === PrivateRoutes.AdminModelProviders),
    ).toHaveLength(1);
    expect(
      paths.filter((p) => p === PrivateRoutes.AdminModelUsageStatistics),
    ).toHaveLength(1);
    expect(
      paths.filter((p) => p === PrivateRoutes.AdminModelTenants),
    ).toHaveLength(1);
  });

  it('skips entries missing path or Component and wraps lazy components with Suspense', async () => {
    const routes = buildUpstreamAdminTree();
    const errorEl = <div>custom-error</div>;
    const customRoutes: PrivateRoute[] = [
      { path: '/admin/missing-component' },
      { Component: async () => ({ default: () => <div>missing-path</div> }) },
      {
        path: '/admin/custom-page',
        Component: async () => ({ default: () => <div>loaded-custom</div> }),
        errorElement: errorEl,
      },
    ];

    const injected = injectPrivateRoutes(routes, customRoutes);
    const navChildren = getNavigationChildren(routes);
    const added = navChildren.find((c) => c.path === '/admin/custom-page');

    expect(injected).toBe(true);
    expect(navChildren.some((c) => c.path === '/admin/missing-component')).toBe(
      false,
    );
    expect(added).toBeDefined();
    expect(added?.errorElement).toBe(errorEl);

    const RouteComponent = added?.Component as React.ComponentType;
    render(<RouteComponent />);
    expect(await screen.findByText('loaded-custom')).toBeInTheDocument();
  });

  it('returns false and leaves routes unchanged when /admin/services anchor is absent', () => {
    const routes: RouteObject[] = [
      {
        path: '/admin',
        children: [
          {
            path: '/admin',
            children: [{ children: [{ path: '/admin/users' }] }],
          },
        ],
      },
    ];

    const injected = injectPrivateRoutes(routes);

    expect(injected).toBe(false);
    expect(routes[0].children?.[0].children?.[0].children).toEqual([
      { path: '/admin/users' },
    ]);
  });
});
