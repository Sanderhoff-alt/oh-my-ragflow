import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, type RenderOptions } from '@testing-library/react';
import type { ReactElement, ReactNode } from 'react';

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: 0,
      },
      mutations: {
        retry: false,
      },
    },
  });
}

export function renderWithQueryClient(
  ui: ReactElement,
  options?: Omit<RenderOptions, 'wrapper'> & { queryClient?: QueryClient },
) {
  const { queryClient = createTestQueryClient(), ...renderOptions } =
    options ?? {};

  function Wrapper({ children }: { children?: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  }

  return {
    queryClient,
    ...render(ui, { wrapper: Wrapper, ...renderOptions }),
  };
}

/**
 * Deterministic translation helper for tests.
 * Returns `key` as-is when no interpolation values are passed, or appends
 * sorted `k=v` pairs so behavioral assertions can verify both the message key
 * and dynamic parameters without coupling to localized copy.
 */
export function formatTestTranslation(
  key: string,
  params?: Record<string, unknown>,
): string {
  if (!params) {
    return key;
  }
  const entries = Object.entries(params)
    .filter(([, value]) => value !== undefined)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, value]) => `${k}=${String(value)}`);
  return entries.length > 0 ? `${key}(${entries.join(',')})` : key;
}
