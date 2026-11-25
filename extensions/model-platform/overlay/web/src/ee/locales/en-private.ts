export default {
  translation: {
    modelPlatform: {
      badge: {
        managed: 'Platform Managed',
      },
      nav: {
        modelProviders: 'Model Providers',
        modelUsage: 'Model Usage',
        tenantCredentials: 'Tenant Credentials',
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
        gatewayHealthyWithLatency: 'Gateway Healthy ({{latency}}ms)',
        gatewayDegraded: 'Gateway Degraded',
        gatewayUnreachable: 'Gateway Unreachable',
        gatewayUnconfigured: 'Gateway Status Unavailable',
        refreshHealth: 'Refresh gateway health',
        gatewayErrorBannerTitle: 'Gateway health check reported an issue',
      },
      actions: {
        rotateKey: 'Rotate Key',
        rotateKeyResume: 'Rotate & Revoke',
        rotateKeyDisabledTitle:
          'Only ready tenants can have their gateway key rotated',
        rotateKeyTitle: 'Rotate tenant virtual key',
        rotateKeyResumeTitle: 'Rotate key and revoke superseded keys',
        rotateKeyDescription:
          'This issues a new gateway virtual key for tenant {{tenantId}}, updates its platform instance with zero downtime, and immediately revokes the previous key.',
        rotateKeyResumeDescription:
          'A previous key rotation for tenant {{tenantId}} left superseded keys pending revocation at the gateway (the background sweeper also retries automatically). Confirming now issues another replacement virtual key and revokes all superseded keys, including the currently active one.',
        rotateKeyCancel: 'Cancel',
        rotateKeyConfirm: 'Confirm rotation',
        rotateKeySuccess:
          'Rotated key for tenant {{tenantId}} (alias: {{alias}}).',
        rotateKeyPendingRevocation:
          'New key is active for tenant {{tenantId}}, but old key revocation is still converging in the background.',
        rotateKeyResultConverged:
          'Key rotated for tenant {{tenantId}} and all superseded keys were revoked.',
        rotateKeyResultPending:
          'New key issued for tenant {{tenantId}}; old key revocation is still converging.',
        rotateKeyAliasLabel: 'Alias: {{alias}}',
        rotateKeyFingerprintLabel: 'Fingerprint: {{fingerprint}}',
        rotateKeyTimestampLabel: 'Rotated at: {{timestamp}}',
        rotateKeyRevokedCount: 'Revoked keys: {{count}}',
      },
      audit: {
        runAction: 'Run Audit',
        rerun: 'Re-run Audit',
        close: 'Close',
        dialogTitle: 'Tenant Spend Isolation Audit',
        dialogSubtitle:
          'Verifying gateway spend logs for tenant {{tenantId}} against platform-managed models.',
        running: 'Running spend isolation audit...',
        failed: 'Audit failed: {{message}}',
        statusLabel: 'Audit Status',
        passed: 'Isolated',
        violationDetected: 'Violation Detected',
        notRunYet: 'Not Audited',
        selectTenantPrompt: 'Enter Tenant ID',
        cardHint: 'Filter by a tenant ID to run a live spend isolation audit.',
        cardSpendSummary: 'Managed {{managed}} / Unmanaged {{selfOwned}}',
        managedSpend: 'Managed Model Spend',
        selfOwnedSpend: 'Unmanaged / Shadow Spend',
        unmanagedModels: 'Unmanaged Models Detected',
        violations: 'Violations',
        alertBannerTitle:
          'Spend isolation violation detected for tenant {{tenantId}}',
      },
      tenants: {
        title: 'Tenant Credentials & Audit',
        subtitle:
          'Inspect managed tenant virtual keys, trigger emergency rotation, and run spend isolation audits.',
        refresh: 'Refresh',
        dismiss: 'Dismiss',
        totalCount: '{{count}} tenant(s)',
        pageLabel: 'Page {{page}}',
        empty: 'No managed tenants found with this status.',
        loadFailed: 'Failed to load tenant credentials: {{message}}',
        filters: {
          status: 'Status',
        },
        statuses: {
          ready: 'Ready',
          degraded: 'Degraded',
          failed: 'Failed',
          pending: 'Pending',
          provisioning: 'Provisioning',
          deleted: 'Deleted',
        },
        convergence: {
          clean: 'Converged',
          dirty: 'Revocation Pending',
        },
        columns: {
          tenant: 'Tenant ID',
          status: 'Status',
          keyAlias: 'Key Alias',
          fingerprint: 'Key Fingerprint',
          lastRotated: 'Last Rotated',
          convergence: 'Key State',
          actions: 'Actions',
        },
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
          showFailedDetails: 'Failed details ({{count}})',
          hideFailedDetails: 'Hide failed details',
          dismiss: 'Dismiss',
          loadFailed: 'Failed to load rollout progress: {{message}}',
          backToFirstPage: 'Back to page 1',
          failedColumns: {
            tenant: 'Tenant ID',
            retries: 'Retries',
            error: 'Last Error',
          },
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
          tenantLabel: 'Tenant ID',
          tenantPlaceholder: 'e.g. tenant_123',
          modelLabel: 'Model Name',
          modelPlaceholder: 'e.g. deepseek-v3',
          startDate: 'Start date',
          endDate: 'End date',
          modelType: 'Model type',
          reset: 'Reset',
        },
        cards: {
          totalSpend: 'Total Incurred Spend',
          totalCalls: '{{value}} total calls',
          cacheSavings: 'Cache savings: {{value}}',
          totalTokens: 'Total Tokens',
          tokenSplit: '{{prompt}} prompt / {{completion}} completion',
          advancedTokens: 'Cached {{cached}} / Reasoning {{reasoning}}',
          totalCharacters: 'Speech Characters (TTS)',
          charactersHint: 'Synthesized characters billed via /v1/audio/speech',
          isolationAudit: 'Spend Isolation Audit',
        },
        trend: {
          title: 'Daily Consumption Trend',
          subtitle: 'Aggregated by usage date for the current filter.',
          cappedHint:
            'Showing latest {{shown}} of {{total}} records; narrow the date range for complete daily totals.',
          spendSeries: 'Spend (USD)',
          tokensSeries: 'Tokens',
          partialSeriesLabel: '{{series}} (partial)',
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
          trendFailed: 'Failed to load daily consumption trend: {{message}}',
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
