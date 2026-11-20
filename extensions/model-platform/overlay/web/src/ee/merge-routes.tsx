import { lazy, Suspense, type ComponentType } from 'react';
import { createBrowserRouter, type RouteObject } from 'react-router';
import { routers } from '@/routes';
import { privateRoutes } from '@/ee/private-routes';

const defaultRouteFallback = (
  <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 backdrop-blur-[1px]">
    <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/70 border-t-transparent" />
  </div>
);

const withLazyRoute = (
  importer: () => Promise<{ default: ComponentType<any> }>,
) => {
  const LazyComponent = lazy(importer);
  return (props: any) => (
    <Suspense fallback={defaultRouteFallback}>
      <LazyComponent {...props} />
    </Suspense>
  );
};

/**
 * Injects model-platform private routes into the admin navigation layout.
 */
function injectPrivateRoutes(routes: RouteObject[]): void {
  for (const route of routes) {
    if (route.path === '/admin' && route.children) {
      for (const authChild of route.children) {
        if (authChild.children) {
          for (const subChild of authChild.children) {
            // navigation-layout has children with /admin/services
            if (subChild.children?.some((c) => c.path === '/admin/services')) {
              for (const pr of privateRoutes) {
                if (
                  pr.path &&
                  pr.Component &&
                  !subChild.children.some((c) => c.path === pr.path)
                ) {
                  subChild.children.push({
                    path: pr.path,
                    Component: withLazyRoute(pr.Component),
                    errorElement: pr.errorElement,
                  });
                }
              }
              return;
            }
          }
        }
      }
    }
    if (route.children) {
      injectPrivateRoutes(route.children);
    }
  }
}

let mergedRouter = routers;

if (routers?.routes) {
  injectPrivateRoutes(routers.routes);
  mergedRouter = createBrowserRouter(routers.routes, {
    basename: import.meta.env.VITE_BASE_URL || '/',
  });
}

export { mergedRouter as routers };
