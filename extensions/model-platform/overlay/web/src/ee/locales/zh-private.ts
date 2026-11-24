export default {
  translation: {
    modelPlatform: {
      badge: {
        managed: '平台托管',
      },
      nav: {
        modelProviders: '模型供应商',
        modelUsage: '模型用量统计',
      },
      modelTypes: {
        all: '全部类型',
        chat: '对话模型',
        embedding: '向量模型',
        asr: '语音识别 (ASR)',
        vision: '视觉模型',
        rerank: '重排模型',
        tts: '语音合成 (TTS)',
        decision: '决策模型',
        unknown: '未知类型',
      },
      status: {
        active: '已启用',
        retired: '已退役',
        gatewayHealthy: '网关正常',
        gatewayDegraded: '网关异常',
        gatewayUnconfigured: '网关状态不可用',
      },
      providers: {
        title: '平台托管模型',
        subtitle: '平台通过统一网关向所有租户提供的托管模型目录。',
        onboardButton: '上架模型',
        poolOverviewTitle: '平台模型池概览',
        poolActiveCount: '{{count}} 个可用',
        poolDefaultLabel: '默认：{{model}}',
        poolNoDefault: '未设置默认',
        columns: {
          model: '模型名称',
          type: '类型',
          status: '状态',
          tenants: '租户分布（可用 / 只读 / 已退役）',
          references: '引用数',
          actions: '操作',
        },
        references: {
          datasets: '{{count}} 个知识库',
          assistants: '{{count}} 个助手',
          none: '—',
        },
        actions: {
          setDefault: '设为默认',
          retire: '退役',
          reactivate: '重新启用',
          retireDisabledTitle: '退役前请先将同类型的其他模型设为默认',
          retryFailed: '重试失败租户',
        },
        empty: '暂无托管模型。请先在网关配置路由，再在此处上架。',
        rollout: {
          running: '下发中',
          finished: '下发完成',
          progress:
            '已完成 {{succeeded}}/{{total}} 个租户，待处理 {{pending}}，失败 {{failed}}',
          usersSummary:
            '（已迁移 {{upgraded}} 名用户，保留 {{preserved}} 名用户的自定义选择）',
        },
        onboardDialog: {
          title: '上架托管模型',
          description:
            '模型必须已在网关中配置路由。上架后会向所有就绪租户分发，且不会更改任何默认设置。',
          placeholder: '网关模型名称，例如 deepseek-v3 或 tts-1',
          cancel: '取消',
          confirm: '确认上架',
        },
        confirmDialog: {
          retireTitle: '退役模型',
          reactivateTitle: '重新启用模型',
          defaultTitle: '设为平台默认模型',
          cancel: '取消',
          confirm: '确认',
          describeDefault:
            '使用平台默认 {{type}} 的用户将切换至 {{model}}，自定义选择其他模型的用户不受影响。',
          describeReactivate: '{{model}} 将在所有租户中重新可用。',
          describeRetireEmbedding:
            '当前有 {{count}} 个知识库正在使用 {{model}}。仍关联知识库的租户将保留只读访问，其余租户将立即移除；以此为默认模型的用户将迁移至平台默认模型。',
          describeRetireAssistant:
            '{{model}} 将立即在所有租户中禁用。当前有 {{count}} 个助手引用此模型，其所有者需要改选其他模型；以此为默认模型的用户将迁移至平台默认模型。',
          describeRetireGeneric:
            '{{model}} 将立即在所有租户中禁用，以此为默认模型的用户将迁移至平台默认模型。',
        },
      },
      usage: {
        title: '模型用量统计',
        subtitle: '按租户与模型类型聚合的 Token、语音字符与费用计量视图。',
        filters: {
          tenantPlaceholder: '按租户 ID 筛选',
          startDate: '开始日期',
          endDate: '结束日期',
          modelType: '模型类型',
          reset: '重置',
        },
        cards: {
          totalSpend: '累计费用',
          totalCalls: '共 {{value}} 次调用',
          totalTokens: '总 Token 消耗',
          tokenSplit: '输入 {{prompt}} / 输出 {{completion}}',
          totalCharacters: '语音合成字符数 (TTS)',
          charactersHint: '通过 /v1/audio/speech 计量的合成字符数',
          selfManaged: '租户自有模型',
          zeroBilled: '零计费',
          selfManagedHint: '租户自配实例严格隔离',
        },
        table: {
          title: '每日用量明细',
          subtitle: '已对账的租户每日各模型调用与消耗记录。',
          totalRecords: '共 {{value}} 条记录',
          columns: {
            date: '日期',
            tenant: '租户',
            model: '模型',
            type: '类型',
            calls: '调用次数',
            usage: 'Token / 字符数',
            spend: '费用 (USD)',
          },
          tokensUnit: '{{value}} tokens',
          charactersUnit: '{{value}} 字符',
          empty: '当前筛选条件下暂无用量记录。',
        },
        error: {
          summaryFailed: '加载用量汇总失败：{{message}}',
          dailyFailed: '加载每日用量明细失败：{{message}}',
          retry: '重试',
        },
        pagination: {
          prev: '上一页',
          next: '下一页',
          pageInfo: '第 {{page}} / {{totalPages}} 页',
        },
      },
    },
  },
};
