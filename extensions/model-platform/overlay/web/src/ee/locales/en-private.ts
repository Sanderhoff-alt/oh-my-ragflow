export default {
  translation: {
    modelPlatform: {
      badge: {
        managed: 'Platform Managed',
      },
      nav: {
        modelProviders: 'Model Providers',
        modelUsage: 'Model Usage',
      },
      modelTypes: {
        all: 'All Types',
        chat: 'Chat',
        embedding: 'Embedding',
        asr: 'Speech to Text (ASR)',
        vision: 'Vision',
        rerank: 'Rerank',
        tts: 'Text-to-Speech (TTS)',
        decision: 'Decision Model',
        unknown: 'Unknown',
      },
      status: {
        active: 'Active',
        retired: 'Retired',
        gatewayHealthy: 'Gateway Healthy',
        gatewayDegraded: 'Gateway Degraded',
        gatewayUnconfigured: 'Gateway Status Unavailable',
      },
      providers: {
        title: 'Platform Managed Models',
        subtitle:
          'Models the platform provides to every tenant through the gateway.',
        onboardButton: 'Onboard model',
        poolOverviewTitle: 'Platform Pool Overview',
        poolActiveCount: '{{count}} active',
        poolDefaultLabel: 'Default: {{model}}',
        poolNoDefault: 'No default set',
        columns: {
          model: 'Model',
          type: 'Type',
          status: 'Status',
          tenants: 'Tenants (active / read-only / retired)',
          references: 'References',
          actions: 'Actions',
        },
        references: {
          datasets: '{{count}} datasets',
          assistants: '{{count}} assistants',
          none: '—',
        },
        actions: {
          setDefault: 'Set default',
          retire: 'Retire',
          reactivate: 'Reactivate',
          retireDisabledTitle:
            'Set another model as default before retiring this one',
          retryFailed: 'Retry failed tenants',
        },
        empty:
          'No managed models yet. Add a route to the gateway, then onboard it here.',
        rollout: {
          running: 'Rolling out',
          finished: 'Rollout finished',
          progress:
            '{{succeeded}}/{{total}} tenants done, {{pending}} pending, {{failed}} failed',
          usersSummary:
            '({{upgraded}} users moved, {{preserved}} kept their own choice)',
        },
        onboardDialog: {
          title: 'Onboard a model',
          description:
            'The model must already be routed by the gateway. Onboarding makes it available to every Ready tenant and never changes a default.',
          placeholder: 'Gateway model name, for example deepseek-v3 or tts-1',
          cancel: 'Cancel',
          confirm: 'Onboard',
        },
        confirmDialog: {
          retireTitle: 'Retire model',
          reactivateTitle: 'Reactivate model',
          defaultTitle: 'Set as platform default',
          cancel: 'Cancel',
          confirm: 'Confirm',
          describeDefault:
            'Platform default users of {{type}} will move to {{model}}. Users who chose a model themselves keep it.',
          describeReactivate:
            '{{model}} will become available again in every tenant.',
          describeRetireEmbedding:
            '{{count}} knowledge base(s) use {{model}}. Tenants that still have knowledge bases keep it read-only, the others lose it at once. Users who default to it move to the platform default.',
          describeRetireAssistant:
            '{{model}} will be disabled in every tenant at once. {{count}} assistant(s) use it and will ask their owners to pick another model. Users who default to it move to the platform default.',
          describeRetireGeneric:
            '{{model}} will be disabled in every tenant at once. Users who default to it move to the platform default.',
        },
      },
      usage: {
        title: 'Model Usage Statistics',
        subtitle:
          'Aggregated token, character, and spend metering across tenants and model types.',
        filters: {
          tenantPlaceholder: 'Filter by tenant ID',
          startDate: 'Start date',
          endDate: 'End date',
          modelType: 'Model type',
          reset: 'Reset',
        },
        cards: {
          totalSpend: 'Total Incurred Spend',
          totalCalls: '{{value}} total calls',
          totalTokens: 'Total Tokens',
          tokenSplit: '{{prompt}} prompt / {{completion}} completion',
          totalCharacters: 'Speech Characters (TTS)',
          charactersHint: 'Synthesized characters billed via /v1/audio/speech',
          selfManaged: 'Tenant Self-Managed',
          zeroBilled: 'Zero-Billed',
          selfManagedHint: 'BYO model instances isolated',
        },
        table: {
          title: 'Daily Consumption Breakdown',
          subtitle: 'Reconciled daily usage records across tenants and models.',
          totalRecords: '{{value}} record(s)',
          columns: {
            date: 'Date',
            tenant: 'Tenant',
            model: 'Model',
            type: 'Type',
            calls: 'Calls',
            usage: 'Tokens / Characters',
            spend: 'Spend (USD)',
          },
          tokensUnit: '{{value}} tokens',
          charactersUnit: '{{value}} chars',
          empty: 'No usage records found for the selected filters.',
        },
        error: {
          summaryFailed: 'Failed to load usage summary: {{message}}',
          dailyFailed: 'Failed to load daily usage records: {{message}}',
          retry: 'Retry',
        },
        pagination: {
          prev: 'Previous',
          next: 'Next',
          pageInfo: 'Page {{page}} of {{totalPages}}',
        },
      },
    },
  },
};
