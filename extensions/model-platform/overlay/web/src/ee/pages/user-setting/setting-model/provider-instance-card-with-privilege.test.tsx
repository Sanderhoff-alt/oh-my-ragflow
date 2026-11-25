import { render, screen } from '@testing-library/react';

import { ProviderInstanceCardWithPrivilege } from './provider-instance-card-with-privilege';

const mockProviderInstanceCard = jest.fn();

jest.mock(
  '@/pages/user-setting/setting-model/instance-card/provider-instance-card',
  () => ({
    ProviderInstanceCard: (props: Record<string, unknown>) => {
      mockProviderInstanceCard(props);
      return (
        <div data-testid="upstream-provider-instance-card">
          {String(props.providerName)}
        </div>
      );
    },
  }),
);

jest.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

function makeInstance(overrides: Record<string, unknown> = {}): any {
  return {
    id: 'inst-1',
    instance_name: 'custom-instance',
    status: '1',
    llm: [],
    ...overrides,
  };
}

describe('ProviderInstanceCardWithPrivilege', () => {
  beforeEach(() => {
    mockProviderInstanceCard.mockClear();
  });

  it('passes non-managed instances straight through without the managed badge', () => {
    const instance = makeInstance({
      instance_name: 'my-openai',
      is_managed: false,
      api_key_config: { is_managed: 'false' },
    });

    render(
      <ProviderInstanceCardWithPrivilege
        providerName="OpenAI-API-Compatible"
        instance={instance}
        defaultOpen={false}
      />,
    );

    expect(
      screen.getByTestId('upstream-provider-instance-card'),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('modelPlatform.badge.managed'),
    ).not.toBeInTheDocument();
    expect(mockProviderInstanceCard).toHaveBeenCalledWith({
      providerName: 'OpenAI-API-Compatible',
      instance,
      defaultOpen: false,
    });
  });

  it.each([
    ['instance_name === "platform"', { instance_name: 'platform' }],
    ['top-level boolean is_managed', { is_managed: true }],
    ['top-level string is_managed', { is_managed: 'true' }],
    [
      'nested boolean api_key_config.is_managed',
      { api_key_config: { is_managed: true } },
    ],
    [
      'nested string api_key_config.is_managed',
      { api_key_config: { is_managed: 'true' } },
    ],
  ])(
    'renders the managed protection badge when detected via %s',
    (_label, overrides) => {
      const instance = makeInstance(overrides);

      render(
        <ProviderInstanceCardWithPrivilege
          providerName="OpenAI-API-Compatible"
          instance={instance}
          defaultOpen={true}
        />,
      );

      expect(
        screen.getByText('modelPlatform.badge.managed'),
      ).toBeInTheDocument();
      expect(mockProviderInstanceCard).toHaveBeenCalledWith({
        providerName: 'OpenAI-API-Compatible',
        instance,
        defaultOpen: true,
      });
    },
  );
});
