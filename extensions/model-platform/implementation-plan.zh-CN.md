# Model Platform 工程实施方案与任务规划 (Implementation Plan & Task Breakdown)

[English](implementation-plan.md) | 简体中文

> [!NOTE]
> **文档定位与职责划分 (Scope & Division of Labor)**：
> - **系统架构规范、核心设计原则与架构决策 (ADR)**：请参阅 [architecture.zh-CN.md](architecture.zh-CN.md)（英文版见 [architecture.md](architecture.md)）。
> - **本文档定位 (Implementation Blueprint)**：专注于具体的**工程落地细节、代码扩展接缝、数据表设计迁移、25 个精细化工程任务规范与端到端测试验证执行指南**。二者相互配合，共同构成完备的扩展技术蓝图。

---

## 目录导航 (Table of Contents)

- [1. 实施原则与分工边界](#1-实施原则与分工边界)
  - [1.1 核心实施目标](#11-核心实施目标)
  - [1.2 六大工程实现原则速览](#12-六大工程实现原则速览)
- [2. 维度一：构建与打包支持 (Build & Packaging Support)](#2-维度一构建与打包支持-build--packaging-support)
  - [2.1 Go ee 全局排他性机制与陷阱](#21-go-ee-构建标签全局排他性机制与陷阱分析)
  - [2.2 排他性补全 Baseline 与 19 包符号划分](#22-排他性补全-baseline-设计与符号边界划分-baseline-partitioning--precedence)
  - [2.3 符号隔离与薄跳板接缝 (Trampolines)](#23-符号隔离与薄跳板接缝-thin-seam-trampolines)
  - [2.4 前端 Web Overlay 接缝机制与变量防线](#24-前端-web-overlay-接缝机制)
  - [2.5 构建期断言与三大 CI 门禁防护](#25-构建期断言与-ci-门禁防护-build-time-ci-guards)
  - [2.6 工作区同步流水线 (apply-overlay.sh)](#26-工作区同步流水线-workspace-overlay-pipeline)
- [3. 维度二：LiteLLM 网关选型与凭证隔离](#3-维度二litellm-网关选型与凭证隔离-litellm-gateway-selection--credential-isolation)
  - [3.1 网关基础设施与本地环回隔离 (127.0.0.1:4000)](#31-网关选型验证与容器网络拓扑隔离)
  - [3.2 平台供应商凭据完全物理隔离](#32-平台供应商凭据完全物理隔离-provider-secret-isolation)
  - [3.3 租户专属虚拟 Key 签发规范与持久化](#33-租户专属虚拟-key-sk-tenant--签发与标识规范)
  - [3.4 网关权限路由与管理端点物理阻断](#34-网关权限路由与管理端点物理阻断)
  - [3.5 租户注销生命周期与虚拟 Key 吊销](#35-租户注销生命周期与非破坏性停用-lifecycle-cleanup--non-destructive-update)
- [4. 维度三：租户模型生命周期与 Reconciler 设计](#4-维度三租户模型生命周期与-reconciler-设计-tenant-model-lifecycle--reconciler-design)
  - [4.1 GORM Post-Commit 异步解耦机制](#41-上游事务钩子分析与-post-commit-异步触发)
  - [4.2 两级重试自愈控制器架构](#42-两级重试自愈控制器架构-two-tier-self-healing-controller)
  - [4.3 状态机模型与持久化结构 (platform_tenant_reconcile)](#43-状态机模型与持久化结构)
  - [4.4 原生驱动无损解析与补偿回滚](#44-原生驱动解析与受管实例数据结构-native-driver-resolution)
  - [4.5 API 级受管模型后端防护 (ManagedProviderGuardMiddleware)](#45-api-级受管提供商与模型后端防护-backend-managed-model-protection)
- [5. 维度四：模型默认值与知识库绑定保护](#5-维度四模型默认值与知识库绑定保护-model-defaults--kb-binding-protection)
  - [5.1 租户默认模型存储结构与机制](#51-租户默认模型存储结构与机制)
  - [5.2 原子条件 SQL 消除默认值初始化并发覆盖 (Atomic Conditional SQL)](#52-原子条件-sql-消除默认值初始化并发覆盖-atomic-conditional-sql)
  - [5.3 显式意图记录消除默认值粘性误判 (Explicit Preference Provenance)](#53-显式意图记录消除默认值粘性误判-explicit-preference-provenance)
  - [5.4 向量模型三段式生命周期与存量知识库保护 (3-Tier Lifecycle for Embedding Models)](#54-向量模型三段式生命周期与存量知识库保护-3-tier-lifecycle-for-embedding-models)
  - [5.5 知识库向量空间绝对不可变性与废弃模型准入防御 (DatasetEmbeddingGuardMiddleware)](#55-知识库向量空间绝对不可变性与废弃模型准入防御-datasetembeddingguardmiddleware)
- [6. 维度五：用量计量与对账系统设计](#6-维度五用量计量与对账设计-usage-metering--reconciliation)
  - [6.1 网关权威 Spend Log 原始日志暂存去重](#63-两层对账流水线与原始日志暂存去重-two-tier-idempotent-metering-pipeline)
  - [6.2 48 小时覆盖重算日账单流水线](#幂等聚合-sql-规范)
  - [6.3 租户自有模型物理零计费隔离验证](#62-租户自有模型调用的物理零计费隔离)
  - [6.4 管理端用量统计与 CSV 导出 API](#64-管理端用量统计与-csv-导出-api)
- [7. 分阶段工程任务拆解 (Phase 0 至 Phase 5，共 25 个精细化工程任务)](#7-分阶段工程任务拆解-phase-0-至-phase-5共-25-个精细化工程任务)
  - [7.1 25 个工程任务全局速查表 (Executive Task Matrix)](#71-25-个工程任务全局速查表-executive-task-matrix)
  - [7.2 任务拆分总览与阶段演进图](#72-任务拆分总览与阶段演进图)
  - [7.3 阶段详细任务清单 (Phase 0 ~ Phase 5)](#73-阶段详细任务清单-phase-0--phase-5)
- [8. 第一期核心能力端到端集成与验证指南](#8-第一期核心能力端到端集成与验证指南-execution--verification-guide)
  - [8.1 目标界定（托管模型首期核心闭环 MVP）](#81-目标界定托管模型首期核心闭环-mvp)
  - [8.2 端到端执行时序图](#82-端到端执行时序图)
  - [8.3 核心故障模式与容灾防护矩阵](#83-核心故障模式与容灾防护矩阵)
  - [8.4 分步落地执行与端到端验证指令](#84-分步落地执行与端到端验证指令)

---

## 1. 实施原则与分工边界

### 1.1 核心实施目标
`model-platform` 扩展旨在为 oh-my-ragflow 引入多租户托管模型平台能力，使平台管理员能够统一向租户提供预置的大语言模型（LLM）与向量嵌入模型（Embedding），同时支持租户继续自由接入自有模型供应商。

### 1.2 六大工程实现原则速览
关于高层架构决策理由与不变式论证，请直接参阅 [architecture.zh-CN.md](architecture.zh-CN.md#3-核心架构不变式保障-core-invariants)。在工程实施落地层面，开发必须严格遵守以下六大硬性约束：
1. **绝对非侵入扩展**：`upstream/ragflow` 检出目录必须保持 100% 原始纯净，`git status --porcelain` 差异严格为空。
2. **原生驱动无损解析**：受管 Provider 统一命名为 `OpenAI-API-Compatible`，实例标识 `platform`，直接复用上游官方驱动，绝不修改上游工厂，绝不触发 DummyModel。
3. **凭据安全与拓扑隔离**：上游供应商真实 Key 物理隔离于 LiteLLM 容器环境；网关仅绑定内部回环 `127.0.0.1:4000`；租户仅持有受管虚拟 Key。
4. **GORM Post-Commit 事务解耦**：严禁在数据库事务钩子中同步调用网关网络接口；基于 GORM `AfterCommit` 事件分发，配备具备 45 秒服务维护重启自愈能力的两级控制器。
5. **原子默认值与不可变知识库**：基于条件原子 SQL 消除并发覆盖；基于偏好表区分显式意图；已解析文档块知识库的向量空间永久不可篡改（HTTP 400 阻断）。
6. **权威 Spend Log 与数学幂等对账**：以 LiteLLM 原始 Spend Log 为权威计量边界，以 `spend_id` 物理去重；日级账单采用天对齐的 48 小时全量覆盖替换重算（`call_count = VALUES(call_count)`），杜绝多重计费。

---

## 2. 维度一：构建与打包支持 (Build & Packaging Support)

### 2.1 Go `ee` 构建标签全局排他性机制与陷阱分析
在 upstream RAGFlow 中，企业版代码受 Go build tags 控制：
- 构建脚本 `upstream/ragflow/build.sh:32-34` 注入环境变量 `RAGFLOW_GO_TAGS`：
  ```bash
  EXTRA_GO_TAGS="${RAGFLOW_GO_TAGS:-}"
  GO_BUILD_TAGS="cgo,static,sonic"
  [ -n "$EXTRA_GO_TAGS" ] && GO_BUILD_TAGS="$GO_BUILD_TAGS,$EXTRA_GO_TAGS"
  ```
- 上游共享代码库中，凡带有 `//go:build !ee` 约束的文件在传入 `-tags ee` 时会被 Go 编译器**全局排除**。
- **全局排他陷阱**：Go build tags 不是针对单个包的局部开关，而是全局构建约束。一旦开启 `RAGFLOW_GO_TAGS=ee`，上游全部带有 `!ee` 的社区桩文件立即失效。如果扩展仅实现了当前功能所需的若干包，编译器将在所有其他未覆盖的包中报错：
  1. 某些包由于仅包含 `stub.go`（例如 `internal/engine/clickhouse` 与 `internal/service/billing`），排除 `!ee` 后目录内不存在任何有效 Go 文件，触发 `build constraints exclude all Go files`。
  2. 依赖未覆盖包的调用方触发 `undefined symbol` 未定义符号编译错误。

### 2.2 排他性补全 Baseline 设计与符号边界划分 (Baseline Partitioning & Precedence)
为了打破该全局陷阱，oh-my-ragflow 建立 **排他性补全 Baseline (`ee-baseline`)**。该 Baseline 针对上游所有受 `!ee` 影响的包，提供带有 `//go:build ee` 约束的社区等价回退桩实现。

#### 符号冲突与边界划分准则 (Symbol Partitioning Rule)
Go 语言严格禁止同一包内出现重复符号声明。`model-platform` 扩展需要通过薄跳板接入 `internal/admin` 与 `internal/router`。为了避免 `ee-baseline` 与薄跳板（`model_platform_trampoline_ee.go`）同时声明同一符号导致 Go 编译器报 `redeclared in this package` 错误，符号声明遵循**严格排他划分与覆盖优先级**：
1. **非路由桩与孤包桩归属 `ee-baseline`**：`ee-baseline` 负责补全 17 个纯桩包，以及 `internal/admin` 与 `internal/router` 中与路由注册无关的社区回退桩（如 `AppendServiceList`, `GetEEServicesStatus`, `ToggleChat` 等）。
2. **路由注册符号独占归属跳板层**：`RegisterEERouter`, `SetupEERouter`, `RegisterEEProtectedRouter`, `RegisterEENoAuthRouter`, `RegisterEEAuthRouter` 等路由接缝符号**严禁在 `ee-baseline` 中声明**，必须独占且唯一地在 `overlay/trampolines/internal/admin/` 与 `overlay/trampolines/internal/router/` 中声明。
3. **Overlay 同步覆盖次序**：`apply-overlay.sh` 按照 `ee-baseline -> trampolines -> backend -> web` 顺序应用。当扩展模块提供某个包的增强实现时，扩展层的文件显式替换或正交补充 baseline。

全量 19 个 Go 源码包与 9 个测试文件的符号划分清单如下：

| # | 源码包路径 | 社区桩源文件 | 包含的核心符号 / 结构体 | 符号归属与处理策略 |
|---|---|---|---|---|
| 1 | `cmd` | `cmd/stub.go` | `runDeepDoc(ctx, serverName, args)` | `ee-baseline` 提供社区空实现 |
| 2 | `internal/admin` | `internal/admin/stub.go` | 服务状态桩与路由注册桩 | **符号划分**：`ee-baseline` 提供 `AppendServiceList`, `GetEEServicesStatus`, `ListUsersEE`, `UpdateServer`, `CheckClientStatus`, `InitLicense`, `CheckServicePeriodically`；`SetupEERouter`, `RegisterEERouter` 独占归属 `trampolines` 薄跳板 |
| 3 | `internal/cli` | `internal/cli/stub.go` | `AdminListUserAgentsCommand` 等 | `ee-baseline` 保持社区空实现/提示 |
| 4 | `internal/common` | `internal/common/stub.go` | `GetRAGFlowType`, `InitAudit`, `OperationLog` 等 | `ee-baseline` 返回社区版标识与无操作日志桩 |
| 5 | `internal/dao` | `internal/dao/stub.go` | 租户与计费迁移桩, `initializeTenantDefaultRoleEE` | `ee-baseline` 提供空安全返回；`initializeTenantDefaultRoleEE` 仅插入 pending 协调记录 |
| 6 | `internal/deepdoc/parser/pdf/table` | `.../table_construct_vh_stub.go` | `ConstructTableVH(cells, boxes, caption)` | `ee-baseline` 返回社区构造逻辑 |
| 7 | `internal/engine/clickhouse` | `internal/engine/clickhouse/stub.go` | `Driver`, `GetDriver()`, `CollectModelUsage`, `Status` | **关键孤包**：`ee-baseline` 必须提供 `Driver` 结构体及 `CollectModelUsage` 空方法，防止包编译为空 |
| 8 | `internal/entity` | `internal/entity/stub.go` | `TenantPermissionManageMembers` 等常量 | `ee-baseline` 导出空常量/类型占位符 |
| 9 | `internal/handler` | `internal/handler/stub.go` | 配额放行桩, ModelGroup 接口桩, 404/501 兜底 | `ee-baseline` 保持社区无限制放行策略或 404/501 兜底 |
| 10 | `internal/permission` | `internal/permission/stub.go` | `newPolicyEE`, `tenantRoleMembershipEnabled` | `ee-baseline` 保持社区默认权限放行逻辑 |
| 11 | `internal/permission/testsupport` | `.../testsupport/stub.go` | `PrepareEnterprisePermissionFixture` | `ee-baseline` 提供测试桩支持 |
| 12 | `internal/router` | `internal/router/stub.go` | 开关函数与路由注册桩 | **符号划分**：`ee-baseline` 提供 `Toggle*` 开关（返回 true）；`SetupEERouter`, `RegisterEE*Router` 独占归属 `trampolines` 薄跳板 |
| 13 | `internal/server` | `internal/server/stub.go` | `StartServer`, `ShutdownServer` | `ee-baseline` 保持生命周期默认空行为 |
| 14 | `internal/server/config` | `internal/server/config/stub.go` | `ModelConfig`, `DefaultModelsConfig`, `BillingConfig` 等 | `ee-baseline` 提供默认配置解析与回退 |
| 15 | `internal/server/local` | `internal/server/local/stub.go` | `IsAdminAvailable()` | `ee-baseline` 返回社区默认状态 |
| 16 | `internal/service` | `internal/service/stub.go` | 租户权限与成员解析桩 | `ee-baseline` 保持社区默认租户解析与账号通道逻辑 |
| 17 | `internal/service/billing` | `internal/service/billing/stub.go` | `Service`, `ResourceDelta`, `InsufficientResourceError` 等 | **关键孤包**：`ee-baseline` 提供空 `Service` 与 `Get() *Service` 返回 nil，`Enabled()` 返回 false |
| 18 | `internal/service/dataset` | `internal/service/dataset/stub.go` | `datasetTestExtraModels` 等 | `ee-baseline` 提供测试桩支持 |
| 19 | `internal/service/document` | `internal/service/document/stub.go` | `grantDocumentOwnerEE` | `ee-baseline` 保持空授权逻辑 |

*伴随排除的 9 个测试文件*：包括 `internal/handler/error_stub_test.go`、`internal/observability/ingestion_log_tdd_test.go`、`internal/common/audit_test.go` 等，在 Baseline 体系中引入等效的单元测试断言，确保编译与基础回归通过。

### 2.3 符号隔离与薄跳板接缝 (Thin Seam Trampolines)
1. **跳板文件位置**：
   - `build/ragflow/internal/admin/model_platform_trampoline_ee.go`（属于 `package admin`）
   - `build/ragflow/internal/router/model_platform_trampoline_ee.go`（属于 `package router`）
2. **符号委托职责**：
   跳板文件仅声明上游桩所要求的符号入口（如 `RegisterEERouter`、`SetupEERouter`），内部直接将其委托给独立的扩展包：
   ```go
   //go:build ee

   package admin

   import (
       "github.com/gin-gonic/gin"
       mpAdmin "ragflow/extensions/model-platform/backend/admin"
   )

   func RegisterEERouter(protected *gin.RouterGroup, r *Router) {
       mpAdmin.RegisterModelPlatformAdminRoutes(protected, r.Engine)
   }
   ```
3. **扩展实现根目录**：
   所有实际的 HTTP 路由处理、服务层、DAO、Reconciler 和安全守卫逻辑均放置于 `extensions/model-platform/backend/...`，具备独立的包名和类型系统，与上游代码物理隔离。

### 2.4 前端 Web Overlay 接缝机制
前端基于 Vite 与 React 构建，采用**文件级覆盖（File-level Web Overlay）**机制：
- **细粒度替换清单**：
  1. `web/src/ee/merge-routes.tsx` 与 `web/src/ee/private-routes.tsx`：注入模型平台管理端路由 `/admin/model-providers` 与 `/admin/model-usage-statistics`。
  2. `web/src/ee/pages/admin/layouts/navigation-private.tsx`：重写 `useEnterpriseAdminNav()`，在管理端侧边栏动态注入模型平台菜单项。
  3. `web/src/ee/utils/api-private.ts`：导出模型平台专有的前端 API 端点，由 `web/src/utils/api.ts` 的 `lodash.merge` 自动合并进主 API 注册表。
  4. `web/src/ee/locales/*-private.ts`：扩展中英文国际化词条字典。
  5. `web/src/ee/pages/user-setting/setting-model/provider-instance-card-with-privilege.tsx`：装饰租户模型设置卡片，高亮标记平台受管模型标签，并禁用对受管实例的删除与编辑操作。
- 未被替换的其他 `web/src/ee/*` 文件继续使用上游社区版的默认回退实现，无副作用。

### 2.5 构建期断言与 CI 门禁防护 (Build-Time CI Guards)
为了杜绝由于环境配置错误或代码漂移导致的构建与运行时灾难，在构建脚本与 CI 流程中引入 **三大硬性前置断言（Pre-Flight Assertions）**：
1. **上游子模块清洁度断言（Submodule Cleanliness Assertion）**：
   构建脚本启动时执行 `git -C upstream/ragflow status --porcelain`。若检测到任何未提交修改或暂存内容，立即中止构建（Exit Code 1），确保上游内核绝对纯净。
2. **前端企业版环境变量禁用断言（Enterprise Flag Guard）**：
   `web/src/pages/admin/utils.tsx:79` 将环境变量绑定为 `IS_ENTERPRISE = import.meta.env.VITE_RAGFLOW_ENTERPRISE === 'RAGFLOW_ENTERPRISE'`。若开启此变量，上游未完成的无效路由（Whitelist/Roles/Monitoring）会导致管理端大面积崩塌。构建脚本强制断言：
   ```bash
   if [ "${VITE_RAGFLOW_ENTERPRISE:-}" = "RAGFLOW_ENTERPRISE" ]; then
       echo "FATAL: VITE_RAGFLOW_ENTERPRISE must NOT be set to RAGFLOW_ENTERPRISE!" >&2
       exit 1
   fi
   ```
3. **19 包基线完整性与符号无冲突断言（Baseline Completeness Assertion）**：
   构建前运行自动化校验器，扫描 `upstream/ragflow` 中所有包含 `//go:build !ee` 的包，断言在 `overlay/ee-baseline/` 或 `overlay/trampolines/` 中存在 100% 对应的 `ee` 声明文件，且 `internal/admin` 与 `internal/router` 不存在符号重复声明。

### 2.6 工作区同步流水线 (Workspace Overlay Pipeline)
`extensions/model-platform/scripts/apply-overlay.sh` 脚本执行流程：
1. **执行前置门禁断言**（检查 upstream clean、环境变量合法性、19 包完整性）。
2. **清洁检出工作区**：
   ```bash
   rm -rf build/ragflow && mkdir -p build/ragflow
   git -C upstream/ragflow archive HEAD | tar -x -C build/ragflow
   ```
3. **分层应用 Overlay**：
   - 复制 `extensions/model-platform/overlay/ee-baseline/` 到 `build/ragflow/`。
   - 复制 `extensions/model-platform/overlay/trampolines/` 到 `build/ragflow/`（注入路由薄跳板）。
   - 复制 `extensions/model-platform/backend/` 到 `build/ragflow/extensions/model-platform/backend/`。
   - 复制 `extensions/model-platform/overlay/web/src/ee/` 到 `build/ragflow/web/src/ee/`。
4. **验证编译**：
   运行 `RAGFLOW_GO_TAGS=ee bash build.sh --go` 与 `VITE_RAGFLOW_ENTERPRISE=false npm --prefix web run build`。

---

## 3. 维度二：LiteLLM 网关选型与凭证隔离 (LiteLLM Gateway Selection & Credential Isolation)

### 3.1 网关选型验证与容器网络拓扑隔离
选择 **LiteLLM Proxy**（固定最新稳定生产版本 `v1.110.0`，镜像 `ghcr.io/berriai/litellm:main-v1.110.0`，Apache 2.0 / MIT 开源许可）：
- **网络拓扑物理隔离**：
  LiteLLM 容器仅加入 Docker 内部网络（如 `ragflow-internal-net`），服务端口绑定至容器内 `4000`，或仅绑定至本地回环接口 `127.0.0.1:4000:4000`。**严禁将 4000 端口映射暴露至公网**。租户无法绕过 RAGFlow 直接向网关发起未受控网络请求。
- **协议兼容性**：提供原生兼容 OpenAI 的 `/v1/chat/completions`、`/v1/embeddings`、`/v1/models`，天然适配 RAGFlow 的 `OpenAI-API-Compatible` 驱动。

### 3.2 平台供应商凭据完全物理隔离 (Provider Secret Isolation)
```mermaid
flowchart TD
    subgraph SecretBoundary [物理安全边界: 内部环境 / 容器编排]
        EnvSecrets[根供应商凭据: OPENAI_API_KEY, DEEPSEEK_API_KEY]
        LiteLLMConfig[LiteLLM config.yaml]
    end

    subgraph InternalNetwork [内部隔离网络: 禁止公网暴露]
        LiteLLMProxy[LiteLLM Proxy 引擎: 127.0.0.1:4000]
        KeyStore[(LiteLLM Postgres Virtual Keys)]
    end

    subgraph RAGFlowControl [RAGFlow 控制面与数据层]
        Reconciler[Model Platform Reconciler]
        TenantDB[(RAGFlow MySQL / Postgres)]
        ModelFactory[RAGFlow ModelFactory]
    end

    EnvSecrets --> LiteLLMProxy
    LiteLLMConfig --> LiteLLMProxy
    Reconciler -->|Master Key 经内部网络调用 /key/generate| LiteLLMProxy
    LiteLLMProxy -->|仅返回虚拟 Key: sk-tenant-xxx| Reconciler
    Reconciler -->|仅持久化虚拟 Key| TenantDB
    ModelFactory -->|通过内部网络发送推理请求| LiteLLMProxy
    LiteLLMProxy -->|使用根凭据代理请求| ExternalProvider[外部模型供应商 OpenAI/DeepSeek]
```
- **隔离实现**：
  - 根供应商真实凭据仅保存在 LiteLLM 容器环境。
  - RAGFlow 数据库 `tenant_model_instance.api_key` 中仅存储针对租户签发的虚拟 Key（形如 `sk-tenant-...`）。
  - 无论通过租户 API、调试日志还是数据库快照，绝无可能获取到上游 Provider 的真实凭据。

### 3.3 租户专属虚拟 Key (`sk-tenant-...`) 签发与标识规范
当 Reconciler 为租户开通服务时，通过 Master Key 调用 LiteLLM 管理接口：
- **调用端点**：`POST /key/generate`
- **请求 Payload 规范**：
  ```json
  {
    "models": ["gpt-4o-mini", "text-embedding-3-small"],
    "team_id": "tenant-6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d",
    "user_id": "user-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d",
    "key_alias": "tenant_tenant-6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d",
    "max_budget": 100.0,
    "budget_duration": "30d",
    "metadata": {
      "tenant_id": "tenant-6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d",
      "managed_by": "oh-my-ragflow-model-platform"
    }
  }
  ```
- **持久化标识要求**：
  LiteLLM `POST /key/update` 要求必须传入 `key`（原始 token）或 `key_alias`。Reconciler 在本地状态表 `platform_tenant_reconcile` 中完整持久化 `gateway_key_alias`（`tenant_<tenantID>`）与 `key_hash`，确保后续白名单变更能精确定位目标凭据。

### 3.4 网关权限路由与管理端点物理阻断
- **管理端点 Master Key 鉴权**：
  LiteLLM 的 `/key/*`、`/team/*`、`/customer/*` 等管理接口严格校验 `Authorization: Bearer <LITELLM_MASTER_KEY>`。租户虚拟 Key 请求管理端点直接被网关以 401/403 阻断。
- **越权模型调用拦截**：
  若租户尝试调用未在 `models` 白名单中的模型，LiteLLM 立即拦截并返回 `403 Forbidden`（`ModelNotAllowedError`），绝不上游透传。

### 3.5 租户注销生命周期与非破坏性停用 (Lifecycle Cleanup & Non-Destructive Update)
1. **模型停用更新（Non-Destructive Update）**：
   管理员从模型池中下架某个模型时，调用 `POST /key/update` 传入缩减后的 `models` 列表，**严禁执行 `POST /key/delete`**，防止中断租户持有的其他正常模型。
2. **租户注销清理（Tenant Deletion Lifecycle）**：
   当租户在 RAGFlow 中被删除注销时，触发异步清理钩子，调用 LiteLLM `POST /key/delete`（传入包含 `keys` 或 `key_aliases` 的 JSON 请求体），彻底吊销并注销该虚拟 Key，回收网关配额，防止产生未受管的僵尸密钥（Zombie Orphan Keys）。
3. **HTTP 客户端超时控制（Worker Starvation Prevention）**：
   Go 扩展包与 LiteLLM 通信的 `http.Client` 必须设置显式超时：
   - 连接/拨号超时（Dial Timeout）：5 秒
   - TLS 握手超时（TLS Handshake Timeout）：5 秒
   - 响应头超时（Response Header Timeout）：10 秒
   - 总体请求超时（Total Timeout）：15 秒
   - 每次 HTTP 调用均传入带有上限的 `context.WithTimeout(ctx, 30*time.Second)`，彻底防止网关挂起时耗尽 Worker Goroutine 线程池。

---

## 4. 维度三：租户模型生命周期与 Reconciler 设计 (Tenant Model Lifecycle & Reconciler Design)

### 4.1 上游事务钩子分析与 Post-Commit 异步触发
审查上游 `internal/service/user.go:242-260`：
```go
if err = db.Transaction(func(tx *gorm.DB) error {
    if err = tx.Create(user).Error; err != nil { return err }
    if err = dao.ApplyDefaultTenantInviteEnabled(ctx, tx, tenant); err != nil { return err }
    if err = tenantDAO.Create(ctx, tx, tenant); err != nil { return err }
    if err = tx.Create(userTenant).Error; err != nil { return err }
    if err = tx.Create(rootFile).Error; err != nil { return err }
    return nil
}); err != nil { return err }
```
- **核心风险**：`tenantDAO.Create` 内部调用了企业版扩展桩 `initializeTenantDefaultRoleEE`。此时外层事务尚未执行 `tx.Create(userTenant)`，本地租户关系尚未完全建立，且事务尚未 Commit。
- **解耦设计**：
  1. `initializeTenantDefaultRoleEE` 在事务 `tx` 内仅执行一条轻量安全的本地 SQL：插入 `platform_tenant_reconcile` 记录，初始状态置为 `pending`。**严禁在钩子内发起任何外部网络请求**。
  2. 异步协调事件的触发点设置在外层事务 `db.Transaction` **完全 Commit 成功之后**（由上层切面或基于事务提交监听器投递至进程内缓冲 channel `chan TenantLifecycleEvent`）。
  3. **预检防御（Pre-Flight Check）**：异步 Worker 接收到事件后，优先查询数据库确认 `tenant` 与 `user_tenant` 行已真实持久化且对外可见。若由于读写分离延迟暂未可见，自动让步并以短退避重试，杜绝脏读与未提交依赖。

### 4.2 两级重试自愈控制器架构 (Two-Tier Self-Healing Controller)
针对网关网络抖动或临时维护重启（如 45 秒服务升级），Reconciler 采用两级自愈机制，**彻底废除“重试 5 次即永久放弃”的缺陷设计**：

```mermaid
flowchart TD
    CommitSuccess["租户事务 Commit 成功"] --> FastQueue["一级快速重试队列: Event Bus"]

    subgraph Tier1 ["一级: 快速抖动恢复 (Fast Queue)"]
        FastQueue --> Worker["并发 Worker 处理"]
        Worker --> CheckCommit{"已提交检查 & 幂等开通"}
        CheckCommit -->|成功| SetReady["标记为 Ready: 协调完成"]
        CheckCommit -->|网络/5xx 失败| FastRetryCheck{"快速重试 < 5 次?"}
        FastRetryCheck -->|是| Backoff1["指数退避 1s, 2s, 4s, 8s, 16s"] --> Worker
        FastRetryCheck -->|否| SetDegraded["标记为 Degraded: 降级保护"]
    end

    subgraph Tier2 ["二级: 水平触发持续自愈 (Level-Triggered Poller)"]
        Ticker["定时巡检 Ticker: 30s"] --> Poller["扫描 Pending 与 Degraded 记录"]
        Poller --> SlowRetryCheck{"检查是否达到最大自愈保留期<br>例如: 7天或50次"}
        SlowRetryCheck -->|在保留期内| SlowBackoff["慢速退避调度: 1m, 2m, 5m, 上限10m"]
        SlowBackoff --> Worker
        SlowRetryCheck -->|超限| SetDead["标记为 Failed: 报警并等待人工介入"]
    end

    subgraph RollbackGuard ["补偿回滚防护"]
        Worker -->|LiteLLM 成功但 DB 写入致命失败| CompDelete["立即调用 LiteLLM POST /key/delete"]
    end
```

- **一级快速重试（Tier 1 Fast Queue）**：
  新租户事件优先由内存队列调度，重试 5 次（间隔 1s, 2s, 4s, 8s, 16s 加随机抖动，累计耗时约 31 秒）。若在此期间网关可用，毫秒级完成初始化。
- **二级持续自愈（Tier 2 Level-Triggered Poller）**：
  若一级重试耗尽（例如 LiteLLM 正在进行 45 秒维护重启），状态转移为 `Degraded`（降级中）。后台 Poller 每 30 秒扫描一次数据库中处于 `pending` 和 `degraded` 的记录，按照长周期退避（1分钟、2分钟、5分钟，上限10分钟）持续重试，最大保留期设为 7 天（或 50 次重试）。一旦网关恢复，受影响的租户自动批量自愈恢复为 `Ready`，无需管理员介入。
- **租约超时与崩溃回收（Provisioning Lease Timeout）**：
  若 Worker 在执行初始化过程中发生进程崩溃或 OOM，处于 `provisioning` 状态的记录将被孤立。Poller 定期检测处于 `provisioning` 且 `updated_at < NOW() - INTERVAL 5 MINUTE` 的租约超时记录，自动重置为 `pending` 或 `degraded`，交由后续重试轮次自愈，杜绝永久悬挂。
- **补偿回滚机制（Compensating Rollback）**：
  若 Reconciler 成功在 LiteLLM 签发了虚拟 Key，但在后续持久化到 RAGFlow 数据库时发生不可恢复的致命异常（如唯一键永久冲突），Worker 必须立即向 LiteLLM 发出 `POST /key/delete`（传入刚签发的 Key 或别名）吊销该 Key，杜绝产生孤儿凭据。
- **多副本防并发抢占（Distributed Row Locking）**：
  在多副本部署环境中，Poller 查询待处理记录时采用数据库行级锁：
  `SELECT * FROM platform_tenant_reconcile WHERE status IN ('pending', 'degraded') AND next_retry_at <= NOW() LIMIT 50 FOR UPDATE SKIP LOCKED`。在不支持 `SKIP LOCKED` 的数据库上采用乐观版本锁（`UPDATE ... SET status = 'provisioning', version = version + 1 WHERE tenant_id = ? AND version = ?`）。

### 4.3 状态机模型与持久化结构
在数据库中建立 `platform_tenant_reconcile` 状态跟踪表：

```sql
CREATE TABLE IF NOT EXISTS platform_tenant_reconcile (
    tenant_id VARCHAR(32) PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'pending', -- 'pending', 'provisioning', 'ready', 'degraded', 'failed', 'deleted'
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 50,
    next_retry_at DATETIME NULL,
    last_error TEXT NULL,
    gateway_key_alias VARCHAR(128) NULL,
    key_hash VARCHAR(128) NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    INDEX idx_status_retry (status, next_retry_at)
);
```

### 4.4 原生驱动解析与受管实例数据结构 (Native Driver Resolution)
#### 解决 `ModelFactory` 驱动分发死锁的核心设计
上游 `internal/entity/models/model.go:317` 在初始化模型实例时执行：
```go
provider.ModelDriver, err = modelFactory.CreateModelDriver(provider.Name, provider.URL, provider.URLSuffix)
```
而在 `internal/entity/models/factory.go:27-240` 中，`CreateModelDriver` 严格以 `strings.ToLower(providerName)` 进行静态 switch 匹配。若使用自定义名称 `"Platform-Models"`，将直接进入 `default:` 分支返回 `NewDummyModel`，所有推理和向量调用均报错 `"not implemented"`。

**解决方案**：
平台托管模型在 RAGFlow 数据库中完全使用上游原生支持的 Provider 名称：
1. **`tenant_model_provider` 表**：
   - `provider_name = "OpenAI-API-Compatible"`
   - `tenant_id = tenantID`
2. **`tenant_model_instance` 表**：
   - `provider_id = <上述 provider.id>`
   - `instance_name = "platform"`（作为平台托管专用实例标识，与租户自建实例区分）
   - `api_key = <LiteLLM 返回的虚拟 Key: sk-tenant-...>`
   - `extra = '{"base_url": "http://litellm-proxy:4000/v1", "is_managed": true}'`
3. **`tenant_model` 表**：
   - 引用上述 instance_id，注册具体模型（`gpt-4o-mini`, `text-embedding-3-small`）。
4. **复合标识生成契约**：
   RAGFlow 核心复合模型标识遵循 `<model_name>@<instance_name>@<provider_name>` 格式。因此平台模型在全系统中的标准标识为：
   - Chat 模型：`gpt-4o-mini@platform@OpenAI-API-Compatible`
   - Embedding 模型：`text-embedding-3-small@platform@OpenAI-API-Compatible`
5. **原生驱动映射效果**：
   当 `CreateModelDriver` 传入 `"OpenAI-API-Compatible"` 时，命中 `factory.go:99` 分支调用 `NewOpenAIAPICompatibleModel(baseURL, urlSuffix)`，完美无损地复用上游官方 OpenAI 协议驱动，**零内核代码侵入，100% 杜绝 DummyModel 死锁**。

### 4.5 API 级受管提供商与模型后端防护 (Backend Managed Model Protection)
仅仅在前端卡片上隐藏删除按钮无法抵御通过 REST API 直接发起的恶意篡改。
- **攻击面分析**：
  上游 `internal/service/model_service.go:257-309` 的 `DeleteModelProvider` 仅校验请求方的租户权限，未对受管记录做任何保护。租户调用 `DELETE /v1/user/setting/model/provider?name=OpenAI-API-Compatible` 即可直接删除平台模型。
- **后端拦截守卫实现**：
  在扩展层对 `DeleteModelProvider`、`UpdateModelProvider` 及模型实例变更接口进行切面拦截或薄跳板前置校验：
  - 检查目标 Provider 实例是否包含 `is_managed == true`，或 `InstanceName == "platform"`。
  - 若为平台托管记录，且请求主体为普通租户用户（非系统平台管理员），**立即强行拦截并返回 HTTP 403 Forbidden**：
    ```json
    {
      "code": 403,
      "message": "Platform-managed providers and models cannot be modified or deleted by tenants."
    }
    ```
  - 彻底杜绝越权篡改与误删隐患。

---

## 5. 维度四：模型默认值与知识库绑定保护 (Model Defaults & KB Binding Protection)

### 5.1 租户默认模型存储结构与机制
在 RAGFlow 中，默认模型在 `user_tenant` 表中按用户成员身份维护（`internal/entity/user_tenant.go:38-42`）：
- `llm_id`（复合模型标识字符串，如 `gpt-4o-mini@platform@OpenAI-API-Compatible`）与 `tenant_llm_id`（指向 `tenant_model.id` 的 UUID）。
- `embd_id` 与 `tenant_embd_id`。

### 5.2 原子条件 SQL 消除默认值初始化并发覆盖 (Atomic Conditional SQL)
严禁在应用层使用“内存读取 -> 判断为空 -> 写入更新”的 Read-Modify-Write 模式，防止并发覆盖用户正在设置的自定义模型。
- **原子条件更新规范**：
  Reconciler 在初始化租户默认值时，必须执行严格的原子条件 SQL：
  ```sql
  -- 初始化默认对话模型 (仅当当前为空时更新)
  UPDATE user_tenant
  SET llm_id = ?,
      tenant_llm_id = ?,
      updated_at = NOW()
  WHERE user_id = ?
    AND tenant_id = ?
    AND (llm_id IS NULL OR llm_id = '');

  -- 初始化默认向量模型 (仅当当前为空时更新)
  UPDATE user_tenant
  SET embd_id = ?,
      tenant_embd_id = ?,
      updated_at = NOW()
  WHERE user_id = ?
    AND tenant_id = ?
    AND (embd_id IS NULL OR embd_id = '');
  ```
- **并发安全保证**：
  如果用户在 Reconciler 执行的毫秒级瞬间在前端主动提交了自定义模型（使得 `llm_id != ''`），数据库引擎在行锁保护下判定条件不成立，`RowsAffected == 0`，用户的显式配置 100% 得到保护，无竞态丢失。

### 5.3 显式意图记录消除默认值粘性误判 (Explicit Preference Provenance)
依赖 `current == old_default` 的启发式规则存在两个致命缺陷：1) 用户主动在 UI 选择了原默认模型会被后续升级误冲刷；2) 错过中间版本迁移的滞后租户会被永久误冻结。
- **偏好来源跟踪表**：
  引入 `platform_user_default_preference` 表，记录用户选择的真实意图：
  ```sql
  CREATE TABLE IF NOT EXISTS platform_user_default_preference (
      id VARCHAR(32) PRIMARY KEY,
      tenant_id VARCHAR(32) NOT NULL,
      user_id VARCHAR(32) NOT NULL,
      model_type INT NOT NULL,
      origin VARCHAR(32) NOT NULL DEFAULT 'platform_default', -- 'platform_default' | 'user_explicit'
      current_model_id VARCHAR(128) NOT NULL,
      created_at DATETIME NOT NULL,
      updated_at DATETIME NOT NULL,
      UNIQUE KEY idx_pref_tenant_user_type (tenant_id, user_id, model_type)
  );
  ```
- **更新规则**：
  1. 用户在前端页面主动保存默认模型时，拦截请求并将对应记录的 `origin` 标记为 `'user_explicit'`。
  2. 管理员全网升级平台推荐默认模型（如从 `gpt-4o-mini` 变更为 `deepseek-v3`）时：
     - 若 `origin == 'user_explicit'`，判定为用户自主意图，**跳过更新**。
     - 若 `origin == 'platform_default'`，且用户当前未脱离平台默认体系，允许安全升级并更新指向。

### 5.4 向量模型三段式生命周期与存量知识库保护 (3-Tier Lifecycle for Embedding Models)
#### 知识库检索瘫痪漏洞的根本根源
上游 `internal/service/model_factory.go:734` 明确规定：
```go
if identity.modelEntity != nil && !strings.EqualFold(identity.modelEntity.Status, "active") {
    return nil, fmt.Errorf("%w: tenant model %q is disabled", errModelConfigUnavailable, modelRef)
}
```
且 LiteLLM 网关若将模型从虚拟 Key 的 `models` 白名单剔除，调用 `/v1/embeddings` 时将返回 HTTP 403。因此，如果管理员停用某个平台 Embedding 模型时，直接将 `tenant_model.status` 置为 `disabled` 或从网关剔除，**存量知识库在用户搜索时将发生灾难性的不可逆查询失败**。

#### 三段式生命周期设计 (Active -> Deprecated/Read-Only -> Tombstoned)
针对 Embedding 模型，实施三段式精细化生命周期管理：

```mermaid
stateDiagram-v2
    [*] --> Active: 管理员上架模型
    Active --> DeprecatedReadOnly: 管理员下架模型 (但存量 KB 引用 > 0)
    Active --> Tombstoned: 管理员下架模型 (且存量 KB 引用 == 0)

    state DeprecatedReadOnly {
        [*] --> PreservedInGateway: 保持 LiteLLM 虚拟 Key 白名单有效
        PreservedInGateway --> ActiveInDB: tenant_model.status 保持 active
        ActiveInDB --> HiddenForNewKBs: 前端与新建 API 排除该模型
    }

    DeprecatedReadOnly --> Tombstoned: 存量 KB 全部迁移或删除 (引用 == 0)

    state Tombstoned {
        [*] --> EvictFromGateway: LiteLLM POST /key/update 剔除白名单
        EvictFromGateway --> DisableInDB: tenant_model.status = disabled
    }

    Tombstoned --> [*]
```

1. **第一阶段：活跃状态（Active）**：
   模型对全量租户开放，可在新建知识库下拉框中选择，网关允许 Embedding 计算。
2. **第二阶段：已废弃只读状态（Deprecated / Read-Only）**：
   - 触发条件：管理员在管理后台停用该模型，但系统检测到存在存量知识库绑定了该模型（`SELECT COUNT(*) FROM knowledgebase WHERE embd_id = M AND status = '1' > 0`）。
   - **保护动作**：
     a) **网关 Key 白名单保留**：不调用 LiteLLM 剔除该模型，允许存量知识库在召回检索时继续生成查询向量。
     b) **模型状态保持 active**：`tenant_model.status` 保持 `"active"`，确保 RAGFlow `ModelFactory` 运行时校验通过。
     c) **新知识库创建屏蔽**：在模型元数据中标记 `is_deprecated = true`。前端下拉框与创建数据集 API 强制过滤废弃模型，禁止任何新知识库绑定该模型。
3. **第三阶段：墓碑注销状态（Tombstoned / Evicted）**：
   - 触发条件：经过租户自主重建或迁移，引用该模型的存量有效知识库数量精确降为 0（`COUNT == 0`）。
   - **注销动作**：调用 LiteLLM `POST /key/update` 将模型安全移出租户虚拟 Key 白名单，并将 `tenant_model.status` 最终置为 `disabled`。

### 5.5 知识库向量空间绝对不可变性与废弃模型准入防御 (DatasetEmbeddingGuardMiddleware)
`DatasetEmbeddingGuardMiddleware`（兼容别名 `DatasetImmutabilityGuardMiddleware`）在 `RegisterEEProtectedRouter` 中统领知识库创建与变更入口：
- **新建知识库准入防御 (`POST /api/v1/datasets`)**：
  若请求指定了已废弃向量模型，或未指定时租户默认向量模型已废弃，直接拦截并返回 `HTTP 400 Bad Request`，禁止任何新知识库绑定废弃模型。
- **空知识库变更防御 (`PUT/PATCH /api/v1/datasets/:id`)**：
  即便知识库没有任何文档切片（`chunk_num == 0 && doc_num == 0`），若试图变更绑定到已废弃模型，同样拦截返回 `HTTP 400 Bad Request`，彻底封闭空库修改绕过路径。
- **存量知识库不可变性防护 (`PUT/PATCH /api/v1/datasets/:id`)**：
  若数据集已包含解析文档或切片（`doc_num > 0 || chunk_num > 0`），严禁任何模型变更操作，直接返回 `HTTP 400 Bad Request`：
  ```json
  {
    "code": 400,
    "message": "Cannot modify embedding model of a dataset that already contains parsed documents or chunks. Create a new dataset to use a different embedding model."
  }
  ```
- 彻底保障底层向量空间的几何一致性与生命周期防御闭环。

---

## 6. 维度五：用量计量与对账设计 (Usage Metering & Reconciliation)

### 6.1 LiteLLM 网关 Spend Log 作为权威计费边界
#### 为什么应用层钩子不能作为计费依据？
审查 `upstream/ragflow/internal/entity/models/base_model.go:148` 可知：
1. 上游广泛存在 `modelUsage = nil` 的调用链路，应用层统计缺失租户上下文。
2. 上游 `clickhouse.Driver.CollectModelUsage` 在开源版仅为 No-op 桩。
3. 应用层在网络超时报错时可能丢弃统计，但底层外部提供商已实际扣费。

LiteLLM Proxy 拦截平台模型的物理流量，并在其内部 Spend Log 中生成原子审计记录，包含 `request_id`（原子 spend ID）、`team_id`（tenant ID）、`model`、`prompt_tokens`、`completion_tokens`、`spend` 等不可篡改字段，是计费的唯一权威真相。

### 6.2 租户自有模型调用的物理零计费隔离
```mermaid
flowchart LR
    subgraph TenantWorkloads [租户多样化模型请求]
        ReqPlatform[平台托管模型调用: gpt-4o-mini]
        ReqCustom[租户自有模型调用: Custom OpenAI / Ollama]
    end

    subgraph RAGFlowRuntime [RAGFlow 运行时 ModelFactory]
        ReqPlatform --> Dispatcher{ModelFactory 路由分发}
        ReqCustom --> Dispatcher
    end

    subgraph DirectCall [自有直连链路: 物理绕行]
        Dispatcher -->|匹配租户私有凭据与 BaseURL| ExternalTenantAPI[租户自有外部供应商 API]
    end

    subgraph GatewayCall [平台受管链路: 完整计量]
        Dispatcher -->|匹配 sk-tenant-xxx & LiteLLM 内部端点| LiteLLMProxy[LiteLLM Proxy 引擎]
        LiteLLMProxy --> AuthoritativeLogs[(LiteLLM Spend Logs 权威日志)]
        LiteLLMProxy --> ProviderAPI[平台签约供应商 OpenAI / DeepSeek]
    end
```
- 租户配置自有 Provider 时，数据存储在自有 `tenant_model_instance` 中，`Extra.base_url` 直指公网或私有推理端点。
- 调用报文**物理上完全不经过 LiteLLM 网关**，网关 Spend Log 产生记录数为 0，平台账单中自有模型费用在数学上恒为 0.00。

### 6.3 两层对账流水线与原始日志暂存去重 (Two-Tier Idempotent Metering Pipeline)
针对网络重试、窗口重叠及 48 小时延迟日志回溯导致的**重复计费灾难（Double-Billing Vulnerability）**，建立不可变原始日志暂存与全量覆盖重算的两层对账架构：

#### 架构分层
1. **第一层：不可变网关原始日志暂存表 (`platform_gateway_spend_log`)**：
   以 LiteLLM 的全局唯一 `spend_id`（或 `request_id`）为主键。日志拉取时执行 `INSERT IGNORE`（或 `ON CONFLICT DO NOTHING`），确保无论拉取多少次、无论时间窗口如何重叠，同一笔调用记录在数据库中严格只存一行。
2. **第二层：确定性覆盖汇总日账单表 (`platform_tenant_usage_daily`)**：
   采用确定性聚合替换（Replacement Upsert）而非增量累加（禁止 `+=`）。在重算指定日期或回溯 48 小时窗口时，直接根据第一层暂存日志重新计算该日期的总和，用新的真实总额原子覆盖旧汇总值。

```mermaid
flowchart TD
    LiteLLM[("LiteLLM Proxy Spend Logs")] -->|"定时拉取窗口日志: 支持回溯 48h"| Collector["对账采集器 Collector"]

    subgraph Tier1_Staging ["第一层: 原始日志暂存去重"]
        Collector -->|"INSERT IGNORE INTO spend_id"| SpendLogTable[("platform_gateway_spend_log<br>唯一键: spend_id")]
    end

    subgraph Tier2_Aggregation ["第二层: 确定性重算覆盖汇总"]
        SpendLogTable -->|"GROUP BY tenant, model, usage_date<br>SUM(tokens), SUM(spend)"| RollupEngine["聚合重算引擎"]
        RollupEngine -->|"ON DUPLICATE KEY UPDATE<br>call_count = VALUES(call_count)"| DailyTable[("platform_tenant_usage_daily<br>联合唯一键: tenant_id, model_name, usage_date")]
    end
```

#### 数据存储表结构
```sql
-- 1. 原始网关日志暂存去重表
CREATE TABLE IF NOT EXISTS platform_gateway_spend_log (
    spend_id VARCHAR(128) PRIMARY KEY, -- LiteLLM request_id / spend_id
    tenant_id VARCHAR(64) NOT NULL,
    api_key VARCHAR(128) NULL,
    key_alias VARCHAR(128) NULL,
    model_name VARCHAR(128) NOT NULL,
    call_type VARCHAR(64) NULL,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    total_tokens INT NOT NULL DEFAULT 0,
    cached_tokens INT NOT NULL DEFAULT 0,
    cache_write_tokens INT NOT NULL DEFAULT 0,
    reasoning_tokens INT NOT NULL DEFAULT 0,
    spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    prompt_spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    completion_spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    cache_savings DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    request_time DATETIME NOT NULL,
    raw_payload TEXT NULL,
    created_at DATETIME NOT NULL,
    INDEX idx_spend_tenant_time (tenant_id, request_time),
    INDEX idx_spend_time (request_time),
    INDEX idx_spend_alias (key_alias),
    INDEX idx_spend_model (model_name)
);

-- 2. 租户每日用量聚合账单表
CREATE TABLE IF NOT EXISTS platform_tenant_usage_daily (
    tenant_id VARCHAR(64) NOT NULL,
    model_name VARCHAR(128) NOT NULL,
    usage_date VARCHAR(10) NOT NULL, -- 格式: YYYY-MM-DD
    call_count BIGINT NOT NULL DEFAULT 0,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens BIGINT NOT NULL DEFAULT 0,
    total_spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    prompt_spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    completion_spend DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    cache_savings DECIMAL(14, 6) NOT NULL DEFAULT 0.000000,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (tenant_id, model_name, usage_date)
);

-- 3. 对账水位记录表
CREATE TABLE IF NOT EXISTS platform_metering_watermark (
    id VARCHAR(32) PRIMARY KEY,
    consumer_name VARCHAR(64) NOT NULL UNIQUE,
    last_reconciled_time DATETIME NOT NULL,
    last_spend_id VARCHAR(128) NULL,
    updated_at DATETIME NOT NULL
);
```

#### 幂等聚合 SQL 规范
在聚合流水线中，计算受影响日期的汇总值并覆盖更新：
```sql
INSERT INTO platform_tenant_usage_daily (
    tenant_id, model_name, usage_date,
    call_count, prompt_tokens, completion_tokens, total_tokens,
    cached_tokens, cache_write_tokens, reasoning_tokens,
    total_spend, prompt_spend, completion_spend, cache_savings,
    created_at, updated_at
)
SELECT
    tenant_id, model_name, DATE_FORMAT(request_time, '%Y-%m-%d'),
    COUNT(*),
    SUM(prompt_tokens),
    SUM(completion_tokens),
    SUM(total_tokens),
    SUM(cached_tokens),
    SUM(cache_write_tokens),
    SUM(reasoning_tokens),
    SUM(spend),
    SUM(prompt_spend),
    SUM(completion_spend),
    SUM(cache_savings),
    NOW(), NOW()
FROM platform_gateway_spend_log
WHERE request_time >= NOW() - INTERVAL 48 HOUR
GROUP BY tenant_id, model_name, DATE_FORMAT(request_time, '%Y-%m-%d')
ON DUPLICATE KEY UPDATE
    call_count = VALUES(call_count),
    prompt_tokens = VALUES(prompt_tokens),
    completion_tokens = VALUES(completion_tokens),
    total_tokens = VALUES(total_tokens),
    cached_tokens = VALUES(cached_tokens),
    cache_write_tokens = VALUES(cache_write_tokens),
    reasoning_tokens = VALUES(reasoning_tokens),
    total_spend = VALUES(total_spend),
    prompt_spend = VALUES(prompt_spend),
    completion_spend = VALUES(completion_spend),
    cache_savings = VALUES(cache_savings),
    updated_at = NOW();
```
**数学幂等保证**：即使对账流水线因网络崩溃反复执行 100 次，或回溯 48 小时覆盖处理迟到日志，日账单数据也绝对保持一致，杜绝任何形式的重叠计费。

---

### 6.4 管理端用量统计与 CSV 导出 API
在 `overlay/trampolines/internal/admin/` 与 `backend/admin/usage_handler.go` 中挂载面向平台管理员的用量审计端点：
- **分页用量查询**：`GET /admin/model-platform/usage/daily`（通过 Nginx/API 网关暴露为 `/api/v1/admin/model-platform/usage/daily`）
  - 支持按租户 ID (`tenant_id`)、模型名称 (`model_name`)、日期范围 (`start_date`, `end_date`) 进行多维过滤与分页。
- **全局用量汇总**：`GET /admin/model-platform/usage/summary`
  - 返回租户级与平台级的调用次数、Token 消耗及账单支出大盘汇总。
- **账单流式 CSV 导出**：`GET /admin/model-platform/usage/export`
  - 采用流式数据传输直接生成并导出 CSV 格式对账报表，包含 `tenant_id`, `model_name`, `usage_date`, `call_count`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `total_spend`，支持百万级历史用量无内存压力导出。
- **自有模型零计费审计**：`GET /admin/model-platform/usage/audit`
  - 自动化审计验证租户自有模型调用产生的网关 Spend Log 严格为零。

---

## 7. 分阶段工程任务拆解 (Phase 0 至 Phase 5，共 25 个精细化工程任务)

### 7.1 25 个工程任务全局速查表 (Executive Task Matrix)

| 阶段 | 任务 ID | 任务核心目标 | 交付形态与关键实现文件 | 验证方式 |
| :--- | :--- | :--- | :--- | :--- |
| **P0: 构建基准** | **TASK-0.1** | 补全 19 包 ee-baseline 桩 | 建立肯定的 `//go:build ee` 社区等价回退桩 (`overlay/ee-baseline/**/stub_ee.go`) | `RAGFLOW_GO_TAGS=ee go build` 编译验证 |
| | **TASK-0.2** | 构建区 Overlay 同步管道 | 自动化同步脚本与三大门禁预检 (`scripts/apply-overlay.sh`) | 脚本执行与 `upstream/ragflow` 纯净度检查 |
| | **TASK-0.3** | 薄跳板接缝与 API 防篡改守卫 | 路由注入薄跳板与受管模型 403 守卫 (`managed_provider_guard.go`, `trampolines/**`) | 单元测试验证 DELETE/PUT 403 阻断 |
| | **TASK-0.4** | 前端 Web Overlay 接缝接入 | 管理端路由与导航注入 (`merge-routes.tsx`, `navigation-private.tsx`) | `VITE_RAGFLOW_ENTERPRISE=false` 打包测试 |
| **P1: 网关基础设施** | **TASK-1.1** | LiteLLM 网关与网络隔离 | 本地环回隔离与容器配置 (`gateway/docker-compose.yml`, `litellm-config.yaml`) | `validate-gateway-config.sh` 与端口校验 |
| | **TASK-1.2** | 平台根凭据安全隔离脱敏 | 真实 API Key 注入与日志脱敏 (`litellm-config.yaml`, `test_secret_isolation.py`) | 凭据隔离断言与控制台输出审查 |
| | **TASK-1.3** | 租户虚拟 Key 客户端与超时控制 | Go HTTP 客户端封装与显式超时机制 (`backend/gateway/client.go`, `types.go`) | 客户端单元测试与超时拦截验证 |
| | **TASK-1.4** | 网关权限路由与管理端点阻断 | 租户 Key 权限范围与端点阻断验证 (`permission_test.go`, `test_gateway_permissions.py`) | 401/403 越权端点与跨模型调用测试 |
| **P2: 租户生命周期** | **TASK-2.1** | 事务解耦与 Post-Commit 异步触发 | GORM 事务提交后异步事件调度 (`backend/dao/tenant_hook.go`, `event/bus.go`) | 事务提交事件分发与预检单元测试 |
| | **TASK-2.2** | 租户协调状态机与数据迁移 | Reconciler 状态表迁移与乐观锁 (`entity/reconcile.go`, `dao/reconcile.go`) | 状态机合法跃迁与乐观锁冲突测试 |
| | **TASK-2.3** | 原生驱动解析与补偿回滚 | `OpenAI-API-Compatible@platform` 同步与失败回滚 (`tenant_reconciler.go`) | 原生驱动匹配与回滚撤销 Key 验证 |
| | **TASK-2.4** | 两级重试控制器与后台 Poller | 一级快速重试 + 二级持续自愈 Poller (`reconciler/poller.go`, `retry.go`) | 模拟 45s 网关停机后的自动恢复测试 |
| | **TASK-2.5** | 租户注销清理与虚拟 Key 吊销 | 租户删除事件捕获与物理 Key 回收 (`tenant_cleanup.go`) | 租户注销生命周期单元测试与物理吊销 |
| **P3: 默认值保护** | **TASK-3.1** | 租户默认模型条件原子 SQL | 条件原子 SQL 初始化默认模型 (`defaults_manager.go`) | 并发覆盖消除与原子更新单元测试 |
| | **TASK-3.2** | 显式意图跟踪与模型选择粘性 | 用户主动偏好跟踪表 (`entity/preference.go`, `sticky_guard.go`) | 平台推荐升级不覆盖显式意图测试 |
| | **TASK-3.3** | 向量模型三段式生命周期保护 | `active` -> `deprecated` -> `tombstoned` 跃迁 (`embedding_lifecycle.go`) | 存量知识库只读检索与新库创建隐藏测试 |
| | **TASK-3.4** | 知识库向量空间绝对不可变守卫 | 阻断修改含块知识库的 embedding 模型 (`dataset_immutability_guard.go`) | 中间件对含块知识库返回 HTTP 400 拦截 |
| **P4: 计量与对账** | **TASK-4.1** | Spend Log 原始日志暂存去重 | 原始日志暂存表与 `INSERT IGNORE` 去重 (`entity/spend_log.go`, `collector.go`) | 重复日志输入物理去重断言测试 |
| | **TASK-4.2** | 48 小时覆盖重算日账单流水线 | 天对齐的滚动窗口覆盖 Upsert 聚合 (`entity/usage.go`, `pipeline.go`) | 连续多次对账账单金额恒等数学幂等测试 |
| | **TASK-4.3** | 租户自有模型零计费物理隔离 | 自有模型直连外部验证 (`isolation_audit.go`) | 自有模型调用产生 0 网关日志与 0 费用审计 |
| | **TASK-4.4** | 管理端用量统计与 CSV 导出 API | 分页统计与流式 CSV 导出接口 (`admin/usage_handler.go`) | HTTP 接口响应与 CSV 流式生成单元测试 |
| **P5: 端到端集成** | **TASK-5.1** | 独立测试集群编排与网络隔离 | PostgreSQL + LiteLLM 测试集群 (`tests/e2e/docker-compose.test.yml`, `setup_env.sh`) | 容器健康探针与网络隔离边界测试 |
| | **TASK-5.2** | 原生驱动端到端业务闭环验证 | Go 原生驱动 Chat + Embedding 真实调用验证 (`tests/e2e/e2e_core_test.go`) | 原生驱动无损解析与持久化验证通过 |
| | **TASK-5.3** | 越权防护、自愈容灾、API 防删测试 | 403 防删、400 防篡改、45s 自愈综合测试 (`e2e_security_stability_test.go`) | 全套安全稳定性 E2E 测试全部 PASS |
| | **TASK-5.4** | 非侵入合规审计与三大 CI 门禁自动化 | 自动化审计脚本与 GitHub Actions 流水线 (`audit-non-invasive.sh`, `ci.yml`) | CI 门禁三大断言 100% 通过 (Exit 0) |

### 7.2 任务拆分总览与阶段演进图

为了确保 model-platform 扩展能够按照经过加固的架构规划稳步落地，整体工程实施被划分为 6 个循序渐进的阶段（Phase 0 至 Phase 5），共计 25 个精细化工程任务：

```mermaid
flowchart TD
    subgraph P0 ["Phase 0: 构建与 Overlay 基础支撑"]
        T0_1["TASK-0.1 补全 19 包 ee-baseline 桩与符号划分"] --> T0_2["TASK-0.2 构建区 Overlay 管道与三大 CI 门禁断言"]
        T0_2 --> T0_3["TASK-0.3 薄跳板接缝与后端 API 级防篡改守卫"]
        T0_3 --> T0_4["TASK-0.4 前端 Web Overlay 接缝与变量防线"]
    end

    subgraph P1 ["Phase 1: 网关基础设施与凭据隔离"]
        T1_1["TASK-1.1 LiteLLM 网关选型验证与容器网络隔离"] --> T1_2["TASK-1.2 供应商根凭据安全隔离与日志脱敏"]
        T1_2 --> T1_3["TASK-1.3 虚拟 Key 客户端与显式网络超时控制"]
        T1_3 --> T1_4["TASK-1.4 网关权限路由与管理端点物理阻断"]
    end

    subgraph P2 ["Phase 2: 租户模型生命周期与 Reconciler"]
        T2_1["TASK-2.1 事务解耦、Post-Commit 触发与提交预检"] --> T2_2["TASK-2.2 租户协调状态机、乐观锁与数据迁移"]
        T2_2 --> T2_3["TASK-2.3 原生驱动解析、受管记录与补偿回滚"]
        T2_3 --> T2_4["TASK-2.4 两级重试自愈控制器与多副本分布式行锁"]
        T2_4 --> T2_5["TASK-2.5 租户注销生命周期清理与虚拟 Key 吊销"]
    end

    subgraph P3 ["Phase 3: 默认值治理与知识库绑定保护"]
        T3_1["TASK-3.1 租户默认模型条件原子 SQL 安全初始化器"] --> T3_2["TASK-3.2 显式意图跟踪表与用户模型选择粘性保护"]
        T3_2 --> T3_3["TASK-3.3 向量模型三段式生命周期与存量知识库保护"]
        T3_3 --> T3_4["TASK-3.4 知识库向量空间绝对不可变性与 API 防御"]
    end

    subgraph P4 ["Phase 4: 用量计量与对账系统"]
        T4_1["TASK-4.1 LiteLLM Spend Logs 原始日志暂存去重"] --> T4_2["TASK-4.2 租户用量日级账单确定性覆盖重算流水线"]
        T4_2 --> T4_3["TASK-4.3 租户自有模型零计费物理隔离与审计验证"]
        T4_3 --> T4_4["TASK-4.4 管理端用量统计与对账导出 API"]
    end

    subgraph P5 ["Phase 5: 端到端集成验证与验收"]
        T5_1["TASK-5.1 测试环境编排与网络隔离套件"] --> T5_2["TASK-5.2 原生驱动端到端业务闭环验证"]
        T5_2 --> T5_3["TASK-5.3 越权防护、自愈容灾、API 防删测试套件"]
        T5_3 --> T5_4["TASK-5.4 非侵入性合规审计与三大 CI 门禁自动化"]
    end

    P0 --> P1
    P1 --> P2
    P2 --> P3
    P3 --> P4
    P4 --> P5
```

---

### 7.3 阶段详细任务清单 (Phase 0 ~ Phase 5)

### Phase 0: 构建与 Overlay 基础支撑 (Build & Overlay Foundation)

#### TASK-0.1: 排他性补全 Baseline (ee-baseline) 架构与 19 包桩定义实现
- **任务描述**：为上游 RAGFlow 中所有受 `//go:build !ee` 影响的 19 个 Go 源码包提供携带 `//go:build ee` 约束的社区等价回退桩实现，彻底解除全局 `-tags ee` 编译死锁，并确立严格的符号划分边界。
- **前置依赖**：无。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/overlay/ee-baseline/`
  - 覆盖包：`cmd/`, `internal/admin/`, `internal/cli/`, `internal/common/`, `internal/dao/`, `internal/deepdoc/parser/pdf/table/`, `internal/engine/clickhouse/` (孤包), `internal/entity/`, `internal/handler/`, `internal/permission/`, `internal/permission/testsupport/`, `internal/router/`, `internal/server/`, `internal/server/config/`, `internal/server/local/`, `internal/service/`, `internal/service/billing/` (孤包), `internal/service/dataset/`, `internal/service/document/`。
- **核心技术要点**：
  1. 准确提取上游各桩中导出的结构体、函数签名与常量，保证类型签名 100% 匹配。
  2. 对于 `internal/engine/clickhouse` 与 `internal/service/billing` 两个仅含桩文件的孤包，必须提供带 `ee` 标签的文件以防目录在编译时被 Go 工具链排除。
  3. **符号排他划分与防冲突约束**：
     - 在 `ee-baseline/internal/admin/` 与 `ee-baseline/internal/router/` 中，**严格排除**路由注册符号声明（`SetupEERouter`, `RegisterEERouter`, `RegisterEEProtectedRouter`, `RegisterEENoAuthRouter`, `RegisterEEAuthRouter`）。
     - 上述路由符号独占保留给跳板层（TASK-0.3），彻底避免 Go 编译器出现 `redeclared in this package` 错误。
- **完成标准**：
  - 各 baseline 桩文件完整覆盖 19 个包中的非路由符号与类型定义；各个 baseline 包在 `//go:build ee` 约束下独立编译通过，不再出现 `build constraints exclude all Go files`。

#### TASK-0.2: 构建区 Overlay 同步管道与三大 CI 门禁断言
- **任务描述**：编写非侵入式构建工作区准备与同步脚本 `apply-overlay.sh`，并在脚本中集成自动化预检断言门禁，严格保证上游内核纯净。
- **前置依赖**：TASK-0.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/scripts/apply-overlay.sh`
  - 工作区根目录：`build/ragflow/`
- **核心技术要点**：
  1. **三大预检断言门禁（Pre-Flight Assertions）**：
     - **断言 1 (Submodule Cleanliness)**：执行 `git -C upstream/ragflow status --porcelain`，若有任何改动立即报错退出（Exit Code 1）。
     - **断言 2 (Enterprise Flag Guard)**：校验环境变量 `VITE_RAGFLOW_ENTERPRISE`，若等于 `RAGFLOW_ENTERPRISE` 立即强行报错中止构建。
     - **断言 3 (19 包基线完整性扫描)**：运行自动化检查脚本，验证 upstream 中全部 19 个 `!ee` 包在 overlay 中均有对应实现。
  2. 使用 `git -C upstream/ragflow archive HEAD | tar -x -C build/ragflow` 导出干净的上游基线代码。
  3. 按照层级依次将 `ee-baseline`、跳板代码、扩展后端代码及前端覆盖文件同步入 `build/ragflow`。
- **完成标准**：
  - 执行 `./extensions/model-platform/scripts/apply-overlay.sh` 后，三项断言全部通过，`build/ragflow` 完整就绪，且 `git status upstream/ragflow` 100% clean。

#### TASK-0.3: 薄跳板接缝、后端扩展包物理隔离与 API 级防篡改守卫
- **任务描述**：在上游 `internal/admin` 与 `internal/router` 注入薄跳板（Thin Trampoline）文件，将控制权无缝委托至模型平台独立后端包；同时在服务层建立拦截守卫，防止租户通过 API 篡改或删除平台受管模型。
- **前置依赖**：TASK-0.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/overlay/trampolines/internal/admin/model_platform_trampoline_ee.go`
  - `extensions/model-platform/overlay/trampolines/internal/router/model_platform_trampoline_ee.go`
  - `extensions/model-platform/backend/admin/`
  - `extensions/model-platform/backend/router/`
  - `extensions/model-platform/backend/guard/managed_provider_guard.go`
- **核心技术要点**：
  1. 跳板文件仅独占声明路由入口函数（如 `RegisterEERouter`），不包含业务数据类型定义。
  2. 扩展后端代码完整置于 `ragflow/extensions/model-platform/backend/...`，具备独立包名。
  3. **API 级受管提供商防篡改守卫**：
     - 在拦截层对 `DeleteModelProvider`、`UpdateModelProvider` 及模型实例删除 API 进行前置校验。
     - 校验目标记录是否带有 `is_managed == true` 或 `instance_name == "platform"`。
     - 若租户用户尝试删除或更新受管 Provider，**直接拦截并返回 HTTP 403 Forbidden**（`{"code": 403, "message": "Platform-managed providers and models cannot be modified or deleted by tenants."}`）。
- **完成标准**：
  - 上游管理端与路由启动时成功通过跳板调用扩展逻辑；租户尝试调用 API 删除平台模型时被 403 阻断；跳板注入完成后，整仓执行 `go vet -tags "cgo,static,sonic,ee" ./...` 编译无未定义符号错误。

#### TASK-0.4: 前端 Web Overlay 接缝接入与企业版环境变量防线
- **任务描述**：为前端实现文件级 Overlay，接入管理端路由与侧边栏菜单，并强制保持 `VITE_RAGFLOW_ENTERPRISE=false`。
- **前置依赖**：TASK-0.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/overlay/web/src/ee/merge-routes.tsx`
  - `extensions/model-platform/overlay/web/src/ee/private-routes.tsx`
  - `extensions/model-platform/overlay/web/src/ee/pages/admin/layouts/navigation-private.tsx`
  - `extensions/model-platform/overlay/web/src/ee/utils/api-private.ts`
  - `extensions/model-platform/overlay/web/src/ee/pages/user-setting/setting-model/provider-instance-card-with-privilege.tsx`
- **核心技术要点**：
  1. 注册管理端独立路由 `/admin/model-providers` 与 `/admin/model-usage-statistics`。
  2. 装饰租户模型卡片，为平台模型展示受管高亮标签，并在 UI 禁用删除与编辑按钮。
  3. 严格禁止开启 `VITE_RAGFLOW_ENTERPRISE=RAGFLOW_ENTERPRISE`，防止激活上游半成品死路由。
- **完成标准**：
  - `npm --prefix build/ragflow/web run build` 打包成功，访问管理后台侧边栏能正常看到并跳转模型管理页面，无 404 死链。

---

### Phase 1: 网关基础设施与凭据隔离 (Gateway & Credential Isolation)

#### TASK-1.1: LiteLLM 网关选型验证、容器化基线配置与内部网络拓扑隔离
- **任务描述**：搭建并固化生产级 LiteLLM Proxy（固定最新稳定版本 `v1.110.0`，镜像 `ghcr.io/berriai/litellm:main-v1.110.0`），定义基于 PostgreSQL 的元数据存储，并严格实施网络拓扑物理隔离。
- **前置依赖**：Phase 0 完成。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/gateway/docker-compose.yml`
  - `extensions/model-platform/gateway/litellm-config.yaml`
- **核心技术要点**：
  1. 验证 LiteLLM Proxy 核心功能遵循 Apache 2.0 / MIT 开源协议。
  2. **网络隔离配置**：
     - LiteLLM 容器仅加入受保护的内部容器网络，端口映射配置为 `127.0.0.1:4000:4000` 或直接通过内部 DNS（`http://litellm-proxy:4000`）寻址。
     - **严禁向公网开放 4000 端口**，杜绝未授权外部直连调用。
  3. 配置预置平台模型池（包含 1 个 Chat 模型与 1 个 Embedding 模型）。
- **完成标准**：
  - LiteLLM 容器健康检查 `GET http://127.0.0.1:4000/health` 返回 200 OK，公网端口扫描无法触达 4000 端口。

#### TASK-1.2: 平台供应商根凭据安全隔离与 LiteLLM 配置
- **任务描述**：确立物理凭据边界，使上游 Provider 真实 API Key 仅存放于网关容器内部，并开启日志脱敏。
- **前置依赖**：TASK-1.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/gateway/litellm-config.yaml`
- **核心技术要点**：
  1. 真实密钥通过 Secret 注入 LiteLLM 容器，绝不进入 RAGFlow 代码库或数据库。
  2. 启用 LiteLLM 凭据脱敏，确保调用报错与控制台输出中的真实密钥均被屏蔽为 `[REDACTED]`。
- **完成标准**：
  - 发起模拟错误请求，检查 LiteLLM 控制台输出与返回体，确认真实根密钥绝无泄露。

#### TASK-1.3: 租户专属虚拟 Key 签发管理客户端与显式网络超时控制
- **任务描述**：在 Go 扩展包中实现与 LiteLLM REST API 交互的客户端，提供 Key 签发、更新与吊销，并设置严格的网络超时。
- **前置依赖**：TASK-1.1, TASK-1.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/gateway/client.go`
  - `extensions/model-platform/backend/gateway/types.go`
- **核心技术要点**：
  1. **超时与线程池保护**：
     - 配置 `http.Client`：`DialTimeout: 5s`, `TLSHandshakeTimeout: 5s`, `ResponseHeaderTimeout: 10s`, 总体 `Timeout: 15s`。
     - 每次请求附带 `context.WithTimeout(ctx, 30*time.Second)`，彻底防止网关挂起时耗尽 Worker 线程池。
  2. 实现 `GenerateKey`：调用 `POST /key/generate`，携带模型白名单、`team_id`（tenant_id）、`user_id` 与 `key_alias`（`tenant_<tenantID>`）。
  3. 实现 `UpdateKey`：调用 `POST /key/update`，支持按 `key_alias` 非破坏性更新授权模型列表。
  4. 实现 `DeleteKey`：调用 `POST /key/delete`（传入包含 `keys` 或 `key_aliases` 的 JSON 请求体），在租户注销时物理销毁虚拟 Key。
  5. 实现 `GetKeyInfo`：调用 `GET /key/info` 支持幂等恢复。
- **完成标准**：
  - 单元测试覆盖各方法，包含对网络超时的精准熔断断言。

#### TASK-1.4: 网关权限路由与管理端点物理阻断验证
- **任务描述**：配置并验证 LiteLLM 路由鉴权策略，阻断租户虚拟 Key 访问管理端点与未授权模型。
- **前置依赖**：TASK-1.3。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/gateway/security_test.go`
- **核心技术要点**：
  1. 验证使用租户虚拟 Key 请求 `POST /key/generate` 等管理端点时被网关物理拦截（返回 401/403）。
  2. 验证使用租户虚拟 Key 调用未授权模型时，网关返回 `403 Forbidden` (`ModelNotAllowedError`)。
- **完成标准**：
  - 安全测试用例全部执行通过，阻断率 100%。

---

### Phase 2: 租户模型生命周期与 Reconciler 设计 (Tenant Lifecycle & Reconciler)

#### TASK-2.1: 事务钩子安全解耦、Post-Commit 异步触发与提交预检
- **任务描述**：将外部网络调用彻底剥离出上游数据库事务，建立事务提交后的异步分发通道，并增加针对已提交数据的预检防御。
- **前置依赖**：Phase 1 完成。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/event/bus.go`
  - `extensions/model-platform/backend/dao/tenant_hook.go`
- **核心技术要点**：
  1. `initializeTenantDefaultRoleEE` 事务钩子内仅执行本地 SQL：向 `platform_tenant_reconcile` 插入 `pending` 状态记录，**绝对不发起网络调用**。
  2. 事件投递严格位于 `UserService.CreateUserTenant` 事务完全 `Commit()` 成功之后，通过进程内缓冲 channel 派发。
  3. **已提交预检机制**：Worker 消费事件时，首先查询数据库核实 `tenant` 与 `user_tenant` 已提交持久化，若未可见则退避等待，杜绝并发脏读与外键违规。
- **完成标准**：
  - 高并发模拟租户注册，数据库事务在 5ms 内提交完成，无事务长连接挂起。

#### TASK-2.2: 租户协调状态机、乐观锁与数据迁移 (`platform_tenant_reconcile`)
- **任务描述**：设计并迁移租户协调状态记录表，定义支持降级自愈的标准状态机流转控制。
- **前置依赖**：TASK-2.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/entity/reconcile.go`
  - `extensions/model-platform/backend/dao/reconcile.go`
  - `extensions/model-platform/backend/migration/001_reconcile_schema.go`
- **核心技术要点**：
  1. `platform_tenant_reconcile` 表字段包含：`tenant_id`, `status` (`pending`, `provisioning`, `ready`, `degraded`, `failed`, `deleted`), `retry_count`, `max_retries`, `next_retry_at`, `gateway_key_alias`, `key_hash`, `version`。
  2. 遵循 `AGENTS.md` GORM 规范，查询严禁使用裸字符串 ID，使用显式参数绑定。
- **完成标准**：
  - 迁移脚本在 MySQL / PostgreSQL 平滑执行，状态机单测通过。

#### TASK-2.3: 原生驱动无损解析、受管模型记录同步与补偿回滚
- **任务描述**：实现单一租户的幂等性开通，使用原生 `OpenAI-API-Compatible` 驱动命名规避 `DummyModel` 死锁，并引入失败补偿回滚机制。
- **前置依赖**：TASK-2.2, TASK-1.3。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/reconciler/tenant_reconciler.go`
- **核心技术要点**：
  1. **驱动原生解析规范**：
     - 在 `tenant_model_provider` 写入 `ProviderName = "OpenAI-API-Compatible"`, `TenantID = tenantID`。
     - 在 `tenant_model_instance` 写入 `InstanceName = "platform"`, `Extra = '{"base_url": "...", "is_managed": true}'`。
     - 生成复合标识：`gpt-4o-mini@platform@OpenAI-API-Compatible` 与 `text-embedding-3-small@platform@OpenAI-API-Compatible`。
     - 上游工厂根据 `"OpenAI-API-Compatible"` 命中 `factory.go:99`，直接加载 `openai_api_compatible.go` 驱动，**绝不回退至 DummyModel**。
  2. **补偿回滚（Compensating Rollback）**：
     - 若 LiteLLM 虚拟 Key 签发成功，但本地数据库持久化发生不可恢复异常，立即调用 LiteLLM `POST /key/delete` 吊销刚生成的 Key，杜绝幽灵凭据。
- **完成标准**：
  - 针对指定测试租户运行协调器，RAGFlow 数据库中各层级受管模型数据完整就绪，重复执行 10 次记录数保持恒定。

#### TASK-2.4: 两级重试自愈控制器与多副本分布式行锁
- **任务描述**：实现两级重试控制器，彻底废除 5 次重试后永久废弃的缺陷，确保网关 45 秒常规重启后能够自动自愈。
- **前置依赖**：TASK-2.3。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/reconciler/poller.go`
  - `extensions/model-platform/backend/reconciler/retry.go`
- **核心技术要点**：
  1. **一级快速队列**：新租户由内存事件调度，执行 5 次快速指数退避重试（1s, 2s, 4s, 8s, 16s）。
  2. **二级后台持续自愈**：
     - 一级重试耗尽后，状态更新为 `degraded`。
     - 后台 Poller（每 30 秒）以慢速指数退避（1m, 2m, 5m，上限 10m）持续重试，最大保留 7 天（或 50 次重试）。
     - 一旦网关从 45 秒重启中恢复，受阻租户自动全部自愈为 `ready`。
  3. **多副本分布式行锁**：
     - Poller 扫描记录时使用 `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 50` 或乐观锁 `version = version + 1`，杜绝多实例重复处理。
- **完成标准**：
  - 模拟 LiteLLM 网关停机 45 秒，验证租户转入 `degraded`，网关恢复后 1 个周期内全部自动转变为 `ready`。

#### TASK-2.5: 租户注销生命周期清理与虚拟 Key 彻底吊销
- **任务描述**：监听租户删除注销事件，异步清理该租户在 LiteLLM 中的虚拟 Key，回收平台额度。
- **前置依赖**：TASK-2.4。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/reconciler/tenant_cleanup.go`
- **核心技术要点**：
  1. 订阅租户删除事件（或由后台定期审计扫描已被软删除/硬删除的租户）。
  2. 查询对应的 `gateway_key_alias`（`tenant_<tenantID>`）或 `key_hash`。
  3. 调用 LiteLLM 客户端 `DeleteKey` 执行 `POST /key/delete`，彻底销毁虚拟凭据并更新状态为 `deleted`。
- **完成标准**：
  - 删除租户后，LiteLLM 中该租户的虚拟 Key 被物理清除，使用该 Key 再次调用网关返回 401。

---

### Phase 3: 模型默认值治理与知识库绑定保护 (Defaults & KB Protection)

#### TASK-3.1: 租户默认模型条件原子 SQL 安全初始化器
- **任务描述**：在租户协调过程中，使用条件原子 SQL 将平台推荐默认模型安全写入 `user_tenant`，杜绝并发覆盖。
- **前置依赖**：Phase 2 完成。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/reconciler/defaults_manager.go`
- **核心技术要点**：
  1. **原子条件 SQL**：
     ```sql
     UPDATE user_tenant
     SET llm_id = ?, tenant_llm_id = ?, updated_at = NOW()
     WHERE user_id = ? AND tenant_id = ? AND (llm_id IS NULL OR llm_id = '');
     ```
  2. 废除应用层内存读写，若用户并发设置了自定义模型，SQL 更新 0 行，用户的选择得到 100% 保护。
- **完成标准**：
  - 模拟并发写入测试，用户在前端主动设置的自定义模型绝对不被后台协调器冲刷。

#### TASK-3.2: 显式意图跟踪表与用户模型选择粘性保护
- **任务描述**：建立意图跟踪表，在平台全局升级默认模型时，准确识别并保护用户的显式自定义选择。
- **前置依赖**：TASK-3.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/entity/preference.go`
  - `extensions/model-platform/backend/reconciler/sticky_guard.go`
- **核心技术要点**：
  1. 创建 `platform_user_default_preference` 表，记录 `(tenant_id, user_id, model_type, origin, current_model_id)`。
  2. 用户在 UI 修改模型时，拦截并标记 `origin = 'user_explicit'`。
  3. 平台推荐升级时，若 `origin == 'user_explicit'` 则严格跳过，彻底消除启发式判断带来的滞后冻结与误覆盖问题。
- **完成标准**：
  - 用户自定义默认模型后，修改平台全局推荐配置并执行 Reconciler，用户的自定义设置 100% 保持不变。

#### TASK-3.3: 向量模型三段式生命周期与存量知识库保护
- **任务描述**：为 Embedding 模型实现 `Active` -> `Deprecated/Read-Only` -> `Tombstoned` 三段式生命周期，防止模型停用导致存量知识库向量检索瘫痪。
- **前置依赖**：TASK-3.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/reconciler/embedding_lifecycle.go`
- **核心技术要点**：
  1. 管理员停用 Embedding 模型时，查询存量有效知识库数量：`COUNT(*) FROM knowledgebase WHERE embd_id = M AND status = '1'`。
  2. **若存量知识库 > 0**：
     - 进入 `Deprecated/Read-Only` 状态。
     - **网关白名单不剔除**该模型，`tenant_model.status` 保持 `"active"`。
     - 在前端与数据集创建 API 中隐藏该模型，禁止新建知识库选择该模型。
     - 存量知识库继续支持向量化检索，绝无 403 阻断或 `model is disabled` 报错。
  3. **当且仅当存量知识库 == 0**：
     - 进入 `Tombstoned` 状态，调用 LiteLLM `POST /key/update` 移出白名单，并将 `tenant_model.status` 更新为 `disabled`。
- **完成标准**：
  - 平台停用向量模型后，存量知识库的搜索检索依然 100% 成功，新建知识库列表中该模型已被屏蔽。

#### TASK-3.4: 知识库向量空间绝对不可变性与 API 层防御
- **任务描述**：在扩展层拦截上游 `UpdateDataset` REST API，严禁向已存在分块的知识库修改 `embedding_model`。
- **前置依赖**：TASK-3.3。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/guard/dataset_immutability_guard.go`
- **核心技术要点**：
  1. 拦截 `PUT /v1/dataset/:id` 处理链路。
  2. 检查目标数据集是否包含切片或文档（`chunk_num > 0 || doc_num > 0`）。
  3. 若数据集非空且请求试图修改 `embedding_model`，**直接返回 HTTP 400 Bad Request**（提示必须重建知识库以更换向量模型）。
  4. Reconciler 内部严禁任何针对 `knowledgebase.embd_id` 的写操作。
- **完成标准**：
  - 调用 API 尝试修改已解析数据集的 Embedding 模型时，后端返回 HTTP 400 拦截，底层向量空间不被篡改。

---

### Phase 4: 用量计量与对账系统 (Usage Metering & Reconciliation)

#### TASK-4.1: LiteLLM Spend Logs 原始日志暂存去重 (`platform_gateway_spend_log`)
- **任务描述**：建立不可变原始网关日志暂存表，以网关 `spend_id` 为唯一主键物理去重，消除重试与窗口重叠风险。
- **前置依赖**：Phase 3 完成。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/metering/collector.go`
  - `extensions/model-platform/backend/entity/spend_log.go`
  - `extensions/model-platform/backend/migration/002_spend_log_schema.go`
- **核心技术要点**：
  1. 建立 `platform_gateway_spend_log` 表，主键为 `spend_id VARCHAR(128)`。
  2. 定期从 LiteLLM `/global/spend/logs` 拉取窗口日志，执行 `INSERT IGNORE` 写入。
  3. 即使因网络重试重复拉取同一批日志 10 次，暂存表中单笔请求依然只存一行。
- **完成标准**：
  - 模拟重复拉取同一日志批次，暂存表行数不发生任何虚增。

#### TASK-4.2: 租户用量日级账单确定性覆盖重算流水线 (`platform_tenant_usage_daily`)
- **任务描述**：实现确定性日账单聚合流水线，使用全量覆盖替换（Replacement Upsert）彻底消除 48 小时回溯导致的重复累计缺陷。
- **前置依赖**：TASK-4.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/metering/pipeline.go`
  - `extensions/model-platform/backend/entity/usage.go`
- **核心技术要点**：
  1. 建立 `platform_tenant_usage_daily` 表，联合唯一索引为 `(tenant_id, model_name, usage_date)`。
  2. **确定性覆盖聚合规范**：
     - 在重新计算近 48 小时窗口账单时，基于第一层暂存日志执行 `SUM()` 分组统计。
     - 执行 `ON DUPLICATE KEY UPDATE call_count = VALUES(call_count), prompt_tokens = VALUES(prompt_tokens), ...`。
     - **严禁使用增量累加（`+=`）**，确保任意回溯与重算操作均具备严格数学幂等性。
- **完成标准**：
  - 模拟 48 小时窗口回溯重算，日账单 Token 汇总值与真实消耗完全一致，无翻倍现象。

#### TASK-4.3: 租户自有模型零计费物理隔离与对账审计验证
- **任务描述**：编写自动化核算验证套件，严格证明租户自有模型调用 100% 绕过网关，平台账单中自有模型费用恒为 0。
- **前置依赖**：TASK-4.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/metering/isolation_audit_test.go`
- **核心技术要点**：
  1. 模拟租户调用自带 OpenAI Key / Ollama 产生 10 万 Token 的对话。
  2. 触发用量对账流水线。
  3. 断言核查：LiteLLM 中该租户的自建模型条目为 0，`platform_tenant_usage_daily` 中自建模型产生的平台开销精确为 0.000000。
- **完成标准**：
  - 隔离性断言审计用例全部通过。

#### TASK-4.4: 管理端用量统计与对账导出 API
- **任务描述**：在管理端扩展路由中提供多租户用量图表数据与明细对账导出 API。
- **前置依赖**：TASK-4.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/backend/admin/usage_handler.go`
- **核心技术要点**：
  1. 提供 `GET /admin/model-platform/usage/daily` 分页与时间范围查询。
  2. 提供 `GET /admin/model-platform/usage/summary` 大盘汇总。
  3. 提供 `GET /admin/model-platform/usage/export` CSV 凭证导出。
  4. 提供 `GET /admin/model-platform/usage/audit` 自有模型零计费审计接口。
- **完成标准**：
  - 前端通过管理端 API 正常拉取并渲染用量图表与明细数据。

---

### Phase 5: 第一期端到端集成验证与验收体系 (E2E Integration & Verification QA)

#### TASK-5.1: 独立测试集群编排与网络隔离套件 (Test Cluster Orchestration)
- **任务描述**：搭建开箱即用的自动化端到端测试集群，一键拉起依赖容器与基线数据库，强制网络隔离。
- **前置依赖**：Phase 0 ~ Phase 4 完成。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/tests/e2e/docker-compose.test.yml`
  - `extensions/model-platform/tests/e2e/setup_env.sh`
- **核心技术要点**：
  1. 包含 LiteLLM Proxy（仅绑定 `127.0.0.1:4000`）、PostgreSQL 与 RAGFlow Server。
  2. 自动运行 Overlay 同步脚本与三大 CI 门禁检查。
- **完成标准**：
  - 执行 `./setup_env.sh` 在 60 秒内初始化就绪，三项断言门禁绿灯通过。

#### TASK-5.2: 原生驱动端到端业务闭环验证 (Chat + Embedding E2E)
- **任务描述**：运行端到端集成测试脚本，覆盖原生 `OpenAI-API-Compatible` 驱动解析、对话问答与知识库向量入库完整闭环。
- **前置依赖**：TASK-5.1。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/tests/e2e/e2e_core_test.go`
- **核心技术要点**：
  1. 创建测试租户，验证 Reconciler 异步完成开通（`ProviderName = "OpenAI-API-Compatible"`, `InstanceName = "platform"`）。
  2. 调用 `ModelFactory` 发起 Chat 生成，验证直接命中 `openai_api_compatible.go`，绝无 `DummyModel` 报错。
  3. 调用 `ModelFactory` 传入文本，断言生成 Embedding 向量维度匹配。
- **完成标准**：
  - 端到端功能测试一次性通过。

#### TASK-5.3: 越权防护、自愈容灾、API 防删与幂等对账测试套件
- **任务描述**：针对核心架构边界执行严格的逆向异常破坏测试，验证系统的健壮性。
- **前置依赖**：TASK-5.2。
- **涉及接缝文件 / 模块**：
  - `extensions/model-platform/tests/e2e/e2e_security_stability_test.go`
- **核心技术要点**：
  1. **API 防删测试**：使用租户 Token 调用 API 删除受管 Provider，断言返回 HTTP 403 Forbidden。
  2. **网关重启自愈测试**：停止 LiteLLM 45 秒，注册新租户确认转入 `degraded`，网关重启后确认自动自愈为 `ready`。
  3. **向量模型停用只读检索测试**：停用平台 Embedding 模型，确认存量知识库依然能正常执行向量检索。
  4. **知识库接口防篡改测试**：尝试修改含块知识库的 `embedding_model`，确认返回 HTTP 400。
  5. **对账幂等测试**：重放 48 小时日志，确认 `platform_tenant_usage_daily` 汇总无重复翻倍。
- **完成标准**：
  - 所有异常与破坏测试用例全部通过，系统无崩溃或数据不一致。

#### TASK-5.4: 非侵入性合规审计与三大 CI 门禁自动化
- **任务描述**：执行独立合规审计脚本，严格校验 upstream 代码未受任何侵入性篡改，并将全流程接入项目 CI 门禁。
- **前置依赖**：TASK-5.3。
- **涉及接缝文件 / 模块**：
  - `.github/workflows/ci.yml`
  - `extensions/model-platform/scripts/audit-non-invasive.sh`
- **核心技术要点**：
  1. 自动化断言：`git -C upstream/ragflow diff --quiet` 为空。
  2. 自动化断言：`VITE_RAGFLOW_ENTERPRISE` 环境变量处于禁用状态。
  3. 自动化断言：19 个包基线桩全覆盖。
- **完成标准**：
  - 审计脚本输出退出码 0，CI 工作流绿灯通过。

---

---

## 8. 第一期核心能力端到端集成与验证指南 (Execution & Verification Guide)

### 8.1 目标界定（托管模型首期核心闭环 MVP）
第一期实施目标（在测试工程代码与目录中标识为 `tests/e2e`）旨在通过最小完备组件打通非侵入式集成的全生命周期闭环：
- **最小模型组合**：
  - 1 个 Chat 模型：`gpt-4o-mini`（通过 `OpenAI-API-Compatible` 驱动调用）。
  - 1 个 Embedding 模型：`text-embedding-3-small`（通过 `OpenAI-API-Compatible` 驱动调用）。
- **验证边界**：
  - Go `ee` 编译补全基线与 Web Overlay 正常生效，三项前置门禁断言全部通过。
  - 租户自动化虚拟 Key 签发与模型记录注入，验证 `is_managed = true` 原生解析。
  - Chat 对话与 Knowledgebase 向量化解析端到端调用通畅。
  - 验证越权访问拦截（HTTP 403）、API 级受管提供商保护、用户默认值粘性、知识库 3 阶段停用保护及用量原始日志去重。


### 8.2 端到端执行时序图
```mermaid
sequenceDiagram
    autonumber
    actor Admin as 平台管理员
    actor User as 测试租户用户
    participant Web as 前端 Web Overlay
    participant Core as RAGFlow 核心服务
    participant DB as RAGFlow 数据库
    participant Reconciler as 扩展 Reconciler
    participant Gateway as LiteLLM Proxy 网关 (内部网络)
    participant UpstreamLLM as 上游真实模型提供商

    Note over Admin, Gateway: 阶段一: 网关基础设施准备与开通 (端口仅绑定内部网络)
    Admin->>Gateway: 部署 LiteLLM (加载 Provider Secrets)
    Gateway-->>Admin: 健康检查 200 OK (127.0.0.1:4000/health)

    Note over User, DB: 阶段二: 租户注册与异步协调 (Post-Commit 双轨自愈)
    User->>Core: 提交注册租户 Tenant-E2E
    Core->>DB: 写入 User / Tenant / UserTenant 事务
    DB-->>Core: 事务提交成功 (Commit)
    Core->>Reconciler: 触发 Post-Commit 异步事件
    Reconciler->>DB: 预检确认 Tenant 和 UserTenant 已真实提交持久化
    Reconciler->>Gateway: POST /key/generate (白名单: chat+embed, team_id=Tenant-E2E)
    Gateway-->>Reconciler: 返回虚拟 Key: sk-tenant-e2e
    Reconciler->>DB: 写入 OpenAI-API-Compatible 供应商与 platform 实例 (is_managed=true)
    Reconciler->>DB: 原子 SQL 初始化 UserTenant 默认空字段 (Fill-If-Empty)
    Reconciler->>DB: 标记 Reconcile 状态为 Ready

    Note over User, UpstreamLLM: 阶段三: 业务功能端到端调用 (原生驱动无损解析)
    User->>Core: 发起对话请求 (使用默认平台 Chat 模型)
    Core->>Gateway: 调用 /v1/chat/completions (携带 sk-tenant-e2e, 映射至 openai_api_compatible.go)
    Gateway->>UpstreamLLM: 代理请求 (携带真实 Provider 根凭据)
    UpstreamLLM-->>Gateway: 返回对话结果与 Token 消耗
    Gateway-->>Core: 透传响应
    Core-->>User: 渲染模型输出

    User->>Core: 创建知识库并上传文档解析 (使用默认平台 Embedding 模型)
    Core->>Gateway: 调用 /v1/embeddings (携带 sk-tenant-e2e)
    Gateway->>UpstreamLLM: 代理向量化请求
    UpstreamLLM-->>Gateway: 返回向量 Embeddings
    Gateway-->>Core: 返回向量并入库
    Core->>DB: 持久化 Knowledgebase (embd_id 强绑定不可变)

    Note over User, Gateway: 阶段四: 安全越权与后端接口保护验证
    User->>Core: 尝试通过 API DELETE 平台 Provider (OpenAI-API-Compatible)
    Core-->>User: 后端校验 is_managed=true -> 拦截并返回 403 Forbidden!
    User->>Gateway: 尝试直接直连调用未经授权的 "claude-3-5-sonnet"
    Gateway-->>User: 拦截并返回 403 Forbidden (ModelNotAllowedError)
    User->>Gateway: 尝试使用 sk-tenant-e2e 调用 /key/generate
    Gateway-->>User: 拦截并返回 401/403 (管理接口物理阻断)

    Note over Admin, DB: 阶段五: 默认值保护、知识库 3 阶段停用与自愈测试
    User->>DB: 用户显式修改默认模型为自定义模型 "custom-chat" (记录 origin=user_explicit)
    Admin->>Reconciler: 修改平台推荐默认模型并触发全局协调
    Reconciler->>DB: 检测到 origin==user_explicit -> 拒绝覆盖, 保留 custom-chat
    Admin->>Reconciler: 停用平台 Embedding 模型
    Reconciler->>DB: 检查存量知识库引用 (引用 > 0) -> 进入 Deprecated/Read-Only 状态
    Note over Reconciler, Gateway: 维持网关白名单与 active 状态, 仅对新建 KB 隐藏, 存量检索 100% 正常!
    Core->>Core: 模拟网关宕机 45 秒 -> 触发 Degraded 状态 -> 网关重启后后台 Poller 自动自愈为 Ready

    Note over Admin, Gateway: 阶段六: 用量对账与自有模型隔离测试 (原始日志暂存去重)
    User->>Core: 调用租户自建的外部 Ollama 供应商
    Core->>UpstreamLLM: 直接调用租户自建端点 (完全绕过 LiteLLM)
    Reconciler->>Gateway: 拉取网关 Spend Logs (回溯 48 小时重试)
    Reconciler->>DB: INSERT IGNORE 写入 platform_gateway_spend_log (spend_id 去重)
    Reconciler->>DB: 确定性覆盖聚合写入 platform_tenant_usage_daily (确认无双重计费, 自有模型开销精准为 0)
```


### 8.3 核心故障模式与容灾防护矩阵
| 故障场景 | 影响范围 | 容灾防护策略 |
|---|---|---|
| 租户注册时 LiteLLM 网关停机或超时 | 租户开通平台模型延时 | 租户本地注册事务正常提交不受阻；异步 Reconciler 进入两级重试队列，一级快速重试失败后转为 `degraded`，网关恢复后由后台 Poller 自动自愈，绝不永久放弃。 |
| 异步开通成功后 RAGFlow 数据库断开 | 凭据写入失败 | 触发补偿回滚机制，立即调用 LiteLLM `POST /key/delete` 注销刚签发的虚拟 Key，防止孤儿凭据产生。 |
| 租户用户被彻底删除/注销 | 虚拟 Key 残留占用配额 | 租户删除事件触发异步清理钩子，调用 LiteLLM `POST /key/delete` 吊销并回收虚拟 Key。 |
| 外部供应商发生网络中断或 504 错误 | 正在进行的推理请求失败 | LiteLLM 透明重试或返回对应 HTTP 状态码；RAGFlow 现有模型驱动按照既有逻辑处理报错，网关仅对实际完成的 Token 计费。 |
| 管理员停用平台 Embedding 模型 | 存量知识库向量检索 | 实施三段式生命周期，存量知识库存在时模型进入 `Deprecated/Read-Only` 状态，网关白名单不剔除，存量检索完全通畅，仅对新库隐藏。 |
| 对账流水线因网络延迟回溯 48 小时重算 | 账单统计数据 | 引入 `platform_gateway_spend_log` 原始暂存表以 `spend_id` 去重；聚合使用覆盖赋值而非增量累加，100% 保障数学幂等。 |
| 租户通过 API 恶意尝试删除受管提供商 | 平台预置模型损坏 | 后端服务层拦截 `DeleteModelProvider`，检测 `is_managed=true` 强行返回 HTTP 403 Forbidden。 |


### 8.4 分步落地执行与端到端验证指令

#### 步骤 1：构建工作区准备与非侵入式基线编译
```bash
# 1. 检出并清理构建区
rm -rf build/ragflow && mkdir -p build/ragflow
git -C upstream/ragflow archive HEAD | tar -x -C build/ragflow

# 2. 应用非侵入式 Overlay 与排他性基线 (自动执行三大预检断言)
bash extensions/model-platform/scripts/apply-overlay.sh

# 3. 校验上游 submodule 保持 100% clean
git -C upstream/ragflow status --porcelain

# 4. 验证 Go ee 构建标签编译
cd build/ragflow
RAGFLOW_GO_TAGS=ee bash build.sh --go

# 5. 验证前端在 VITE_RAGFLOW_ENTERPRISE=false 条件下打包
cd web
VITE_RAGFLOW_ENTERPRISE=false npm run build
cd ../../..
```

#### 步骤 2：启动 LiteLLM 测试集群并校验网络隔离
```bash
# 启动 E2E 测试集群 (包含 PostgreSQL 数据库与 LiteLLM 网关，仅绑定 127.0.0.1:4000)
docker compose -f extensions/model-platform/tests/e2e/docker-compose.test.yml up -d

# 检查网关内部健康与存活探针
curl -s -f http://127.0.0.1:4000/health/liveliness || exit 1
```

#### 步骤 3：模拟签发租户专用虚拟 Key 与标识验证
```bash
TENANT_KEY_JSON=$(curl -s -X POST "http://127.0.0.1:4000/key/generate" \
  -H "Authorization: Bearer sk-master-e2e-platform-test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "models": ["gpt-4o-mini", "text-embedding-3-small"],
    "team_id": "tenant-e2e-core",
    "user_id": "user-e2e-core",
    "key_alias": "tenant_tenant-e2e-core",
    "metadata": {"tenant_id": "tenant-e2e-core"}
  }')

echo "Generated Key Response: $TENANT_KEY_JSON"
TENANT_VIRTUAL_KEY=$(echo "$TENANT_KEY_JSON" | jq -r .key)
echo "Tenant Virtual Key: $TENANT_VIRTUAL_KEY"
```

#### 步骤 4：越权防护、API 级后端防删与隔离校验
```bash
# 1. 验证受管模型 API 防删 (租户尝试删除受管 Provider, 必须返回 403 Forbidden)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "http://localhost:8080/v1/user/setting/model/provider?name=OpenAI-API-Compatible" \
  -H "Authorization: Bearer <tenant_user_token>")
echo "Backend Managed Model API Delete Guard (Expect 403): $STATUS_CODE"

# 2. 验证管理端点物理阻断 (使用租户 Key 访问 /key/generate, 必须返回 401/403)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "http://127.0.0.1:4000/key/generate" \
  -H "Authorization: Bearer $TENANT_VIRTUAL_KEY" \
  -H "Content-Type: application/json" \
  -d '{"models": ["gpt-4o"]}')
echo "Admin Endpoint Block Check (Expect 401 or 403): $STATUS_CODE"

# 3. 验证未授权模型阻断 (请求未被授权的 deepseek-v3, 必须返回 403)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "http://127.0.0.1:4000/v1/chat/completions" \
  -H "Authorization: Bearer $TENANT_VIRTUAL_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "deepseek-v3", "messages": [{"role":"user","content":"ping"}]}')
echo "Unauthorized Model Block Check (Expect 403): $STATUS_CODE"

# 4. 验证知识库 Embedding 模型不可变性防线 (含文档块知识库修改 embedding_model, 必须返回 400)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "http://localhost:8080/v1/dataset/ds-e2e-test" \
  -H "Authorization: Bearer <tenant_user_token>" \
  -H "Content-Type: application/json" \
  -d '{"embedding_model": "other-embedding-model"}')
echo "Dataset Immutability Guard (Expect 400): $STATUS_CODE"
```

#### 步骤 5：原始日志去重暂存与 48 小时覆盖重算验证
```bash
# 1. 查询 LiteLLM 权威 Spend Logs
curl -s -X GET "http://127.0.0.1:4000/global/spend/logs" \
  -H "Authorization: Bearer sk-master-e2e-platform-test-key" | jq .

# 2. 查询管理端日级用量统计
curl -s -X GET "http://localhost:8080/api/v1/admin/model-platform/usage?tenant_id=tenant-e2e-core" \
  -H "Authorization: Bearer <admin_token>" | jq .
```

#### 步骤 6：Go 原生驱动端到端集成测试套件验证
```bash
# 执行原生驱动端到端核心测试 (原生驱动分发、真实网关与 PostgreSQL、原子默认值、三段式生命周期、48h 幂等重算、零计费审计)
go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestE2E"

# 执行越权防护、自愈容灾、API 防删与数据集不可变性测试
go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestSecurityStability"
```

#### 步骤 7：非侵入性合规审计与三大 CI 门禁自动化
```bash
# 运行非侵入合规性自动化审计脚本
bash extensions/model-platform/scripts/audit-non-invasive.sh
```
