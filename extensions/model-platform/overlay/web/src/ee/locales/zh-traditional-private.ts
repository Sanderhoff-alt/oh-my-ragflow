export default {
  translation: {
    modelPlatform: {
      badge: {
        managed: '平台託管',
      },
      nav: {
        modelProviders: '模型供應商',
        modelUsage: '模型用量統計',
      },
      modelTypes: {
        all: '全部類型',
        chat: '對話模型',
        embedding: '向量模型',
        asr: '語音辨識 (ASR)',
        vision: '視覺模型',
        rerank: '重排模型',
        tts: '語音合成 (TTS)',
        decision: '決策模型',
        unknown: '未知類型',
      },
      status: {
        active: '已啟用',
        retired: '已退役',
        gatewayHealthy: '閘道正常',
        gatewayDegraded: '閘道異常',
        gatewayUnconfigured: '閘道狀態不可用',
      },
      providers: {
        title: '平台託管模型',
        subtitle: '平台透過統一閘道向所有租戶提供的託管模型目錄。',
        onboardButton: '上架模型',
        poolOverviewTitle: '平台模型池概覽',
        poolActiveCount: '{{count}} 個可用',
        poolDefaultLabel: '預設：{{model}}',
        poolNoDefault: '未設定預設',
        columns: {
          model: '模型名稱',
          type: '類型',
          status: '狀態',
          tenants: '租戶分佈（可用 / 唯讀 / 已退役）',
          references: '引用數',
          actions: '操作',
        },
        references: {
          datasets: '{{count}} 個知識庫',
          assistants: '{{count}} 個助手',
          none: '—',
        },
        actions: {
          setDefault: '設為預設',
          retire: '退役',
          reactivate: '重新啟用',
          retireDisabledTitle: '退役前請先將同類型的其他模型設為預設',
          retryFailed: '重試失敗租戶',
        },
        empty: '暫無託管模型。請先在閘道設定路由，再於此處上架。',
        rollout: {
          running: '下發中',
          finished: '下發完成',
          progress:
            '已完成 {{succeeded}}/{{total}} 個租戶，待處理 {{pending}}，失敗 {{failed}}',
          usersSummary:
            '（已遷移 {{upgraded}} 名使用者，保留 {{preserved}} 名使用者的自訂選擇）',
        },
        onboardDialog: {
          title: '上架託管模型',
          description:
            '模型必須已在閘道中設定路由。上架後會向所有就緒租戶分發，且不會變更任何預設設定。',
          placeholder: '閘道模型名稱，例如 deepseek-v3 或 tts-1',
          cancel: '取消',
          confirm: '確認上架',
        },
        confirmDialog: {
          retireTitle: '退役模型',
          reactivateTitle: '重新啟用模型',
          defaultTitle: '設為平台預設模型',
          cancel: '取消',
          confirm: '確認',
          describeDefault:
            '使用平台預設 {{type}} 的使用者將切換至 {{model}}，自訂選擇其他模型的使用者不受影響。',
          describeReactivate: '{{model}} 將在所有租戶中重新可用。',
          describeRetireEmbedding:
            '目前有 {{count}} 個知識庫正在使用 {{model}}。仍關聯知識庫的租戶將保留唯讀存取，其餘租戶將立即移除；以此為預設模型的使用者將遷移至平台預設模型。',
          describeRetireAssistant:
            '{{model}} 將立即在所有租戶中停用。目前有 {{count}} 個助手引用此模型，其擁有者需要改選其他模型；以此為預設模型的使用者將遷移至平台預設模型。',
          describeRetireGeneric:
            '{{model}} 將立即在所有租戶中停用，以此為預設模型的使用者將遷移至平台預設模型。',
        },
      },
      usage: {
        title: '模型用量統計',
        subtitle: '按租戶與模型類型彙總的 Token、語音字元與費用計量檢視。',
        filters: {
          tenantPlaceholder: '按租戶 ID 篩選',
          startDate: '開始日期',
          endDate: '結束日期',
          modelType: '模型類型',
          reset: '重設',
        },
        cards: {
          totalSpend: '累計費用',
          totalCalls: '共 {{value}} 次呼叫',
          totalTokens: '總 Token 消耗',
          tokenSplit: '輸入 {{prompt}} / 輸出 {{completion}}',
          totalCharacters: '語音合成字元數 (TTS)',
          charactersHint: '透過 /v1/audio/speech 計量的合成字元數',
          selfManaged: '租戶自有模型',
          zeroBilled: '零計費',
          selfManagedHint: '租戶自配實例嚴格隔離',
        },
        table: {
          title: '每日用量明細',
          subtitle: '已對帳的租戶每日各模型呼叫與消耗記錄。',
          totalRecords: '共 {{value}} 筆記錄',
          columns: {
            date: '日期',
            tenant: '租戶',
            model: '模型',
            type: '類型',
            calls: '呼叫次數',
            usage: 'Token / 字元數',
            spend: '費用 (USD)',
          },
          tokensUnit: '{{value}} tokens',
          charactersUnit: '{{value}} 字元',
          empty: '目前篩選條件下暫無用量記錄。',
        },
        error: {
          summaryFailed: '載入用量彙總失敗：{{message}}',
          dailyFailed: '載入每日用量明細失敗：{{message}}',
          retry: '重試',
        },
        pagination: {
          prev: '上一頁',
          next: '下一頁',
          pageInfo: '第 {{page}} / {{totalPages}} 頁',
        },
      },
    },
  },
};
