# 模型平台扩展

[English](README.md) | 简体中文

`model-platform` 扩展通过隔离部署的 LiteLLM 模型网关，为 RAGFlow 提供集中托管、按租户隔离的平台模型能力，同时完整保留各租户配置自有密钥（BYOK，Bring Your Own Key）供应商的自由度。平台模型消耗在网关层进行权威归属与计量；租户自有 BYOK 流量在物理链路上直接访问外部上游，不经过网关，产生严格 `$0.00` 平台费用。

**状态：** 生产就绪（Phase 0–5 全部完成并合并，已包含动态模型目录、应急密钥轮换、自愈计量与 Admin 治理控制台）。

## 核心能力亮点

- **物理凭据隔离**：上游根密钥严格驻留在仅监听环回地址（`127.0.0.1:4000`）的 LiteLLM 网关内部；每个租户仅获得受限虚拟密钥（`sk-tenant-*`），挂载在其托管的 `platform` 实例上。
- **支持 7 类托管模型**：覆盖 `chat`（1）、`embedding`（2）、`asr`（4）、`vision`（8）、`rerank`（16）、`tts`（32）与 `decision`（128）。本地 `ocr`（64）流水线保持在网关之外运行。
- **动态模型目录与全租户滚动下发**：管理员可通过 Admin 控制台或 Admin API 上架、退役、重新启用托管模型以及切换平台默认模型，系统异步向所有就绪租户收敛，并提供实时进度追踪与失败租户一键重试。
- **租户防护与 BYOK 零平台计费**：
  - 前端（`ProviderInstanceCardWithPrivilege`）与后端（`ManagedProviderGuardMiddleware`）双重防护，禁止租户修改或删除平台托管的 `platform` 实例。
  - `DefaultModelPreferenceMiddleware` + `StickyGuard` 保护用户显式选择的默认模型，避免被平台默认模型升级覆盖。
  - `DatasetImmutabilityGuardMiddleware` 冻结非空知识库的向量模型，并阻止新建或空知识库绑定已退役的向量模型，同时保障已有知识库的只读检索不受影响。
  - 租户自有 BYOK 供应商直连外部上游端点，产生 `$0.00` 平台费用。
- **权威计量与消费隔离审计**：增量水位线采集、按日历日幂等替换汇总（含输入/输出/缓存命中/缓存写入/推理 Token、TTS 语音合成字符数、费用与缓存节省金额）、故障后自动多日补齐、可选原始日志保留清理以及实时影子消费隔离审计。
- **零中断应急密钥轮换**：支持在 Admin 控制台或通过 API 对单租户零中断轮换密钥，同时提供带并发与速率限制的批量轮换脚本 `scripts/emergency-rotate-keys.sh`，后台自动完成旧密钥吊销收敛。

## 快速开始

1. **启动隔离部署的 LiteLLM 网关**：

   ```sh
   cd extensions/model-platform/gateway
   cp .env.example .env
   # 填写 POSTGRES_PASSWORD、LITELLM_MASTER_KEY、LITELLM_SALT_KEY 与上游供应商密钥
   ./validate-gateway-config.sh
   docker compose up -d
   ```

2. **组装并构建增强版 RAGFlow 发行工作区**（保持 `upstream/ragflow` 100% 洁净）：

   ```sh
   ./extensions/model-platform/scripts/apply-overlay.sh
   ```

3. **配置 RAGFlow API 与 Admin 服务环境变量**：

   ```sh
   export LITELLM_BASE_URL=http://127.0.0.1:4000
   export LITELLM_MASTER_KEY=<与网关一致的-master-key>
   ```

   启动增强版 RAGFlow API 与 Admin 服务后，在 `gateway/litellm-config.yaml` 中添加模型路由，随后进入 **Admin 控制台 → 模型供应商**（`/admin/model-providers`）上架模型即可。

## 文档导航

| 目标读者 | 简体中文 | English | 内容范围 |
| :--- | :--- | :--- | :--- |
| **平台运维与超级管理员** | [管理员与运维指南](docs/admin-guide.zh-CN.md) | [Admin & Operator Guide](docs/admin-guide.md) | 网络隔离拓扑、Docker 生产部署、环境变量参考、Admin 控制台界面操作手册、批量密钥轮换与故障恢复手册 |
| **租户 Owner、管理员与普通用户** | [租户与用户指南](docs/user-guide.zh-CN.md) | [Tenant & User Guide](docs/user-guide.md) | 租户角色权限（`owner` / `admin` / `normal`）、平台模型与 BYOK 对比、Web 界面操作指南、粘性默认模型、知识库向量不可变规则与 FAQ |
| **集成开发与自动化脚本作者** | [API 参考文档](docs/api-reference.zh-CN.md) | [API Reference](docs/api-reference.md) | 全部 13 个 Admin 新增接口的完整字段契约，以及 App API 侧 3 个守卫中间件的行为规则与错误码 |
| **架构师与项目贡献者** | [架构设计](architecture.zh-CN.md) | [Architecture Design](architecture.md) | 系统拓扑边界、核心不变式与架构决策记录（ADR 1–6） |
| **项目贡献者** | [工程实施计划](implementation-plan.zh-CN.md) | [Implementation Plan](implementation-plan.md) | Phase 0–5 工程路线图、数据库迁移与端到端验证方案 |
| **仓库贡献者** | [RAGFlow 扩展点与集成规则](../../docs/extension-points.zh-CN.md) | [RAGFlow Extension Points](../../docs/extension-points.md) | 非侵入式集成约定与上游扩展接缝 |
