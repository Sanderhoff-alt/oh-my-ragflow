# 平台管理员与运维指南

[English](admin-guide.md) | 简体中文

本指南面向负责部署、配置、监控与治理 `model-platform` 扩展的**平台运维人员（SRE）**与 **RAGFlow 超级管理员（Superuser）**。

> [!NOTE]
> 如需查看全部管理接口的字段级请求/响应契约与 `curl` 示例，请参阅 [API 参考文档](api-reference.zh-CN.md)。如需了解租户侧界面使用方式与行为规则，请参阅 [租户与用户指南](user-guide.zh-CN.md)。

---

## 1. 架构与网络隔离拓扑

本扩展在 RAGFlow 租户与上游模型供应商之间建立了严格的物理凭据隔离边界：

```mermaid
flowchart LR
    AdminUI["Admin 控制台界面<br/>(/admin/model-*)"] -->|"Superuser Token<br/>/api/v1/admin/model-platform/*"| AdminSvc["RAGFlow Admin 服务<br/>(:9381)"]
    APISvc["RAGFlow API 服务<br/>(:9380) + 后台 Worker"] -->|"Master Key（签发/吊销/计量）"| Gateway["LiteLLM 代理网关<br/>仅绑定 127.0.0.1:4000"]
    AdminSvc -->|"Master Key（健康/目录/轮换）"| Gateway
    APISvc -->|"租户虚拟密钥<br/>(sk-tenant-*)"| Gateway
    Gateway -->|"根供应商密钥<br/>(${PLATFORM_UPSTREAM_*})"| Upstream["上游模型供应商<br/>(OpenAI / DeepSeek 等)"]
    Gateway --- PG[("独立 LiteLLM<br/>PostgreSQL 16")]
    APISvc -->|"租户 BYOK 密钥<br/>（物理直连旁路，$0.00 平台费用）"| Upstream
```

### 安全与生命周期核心保障

1. **仅环回地址监听**：`gateway/docker-compose.yml` 强制将 LiteLLM 绑定到 `127.0.0.1:4000:4000`。严禁暴露 `0.0.0.0:4000` 或将公网流量直接路由至 LiteLLM，`gateway/validate-gateway-config.sh` 会在部署前强制校验该规则。
2. **根密钥零泄漏**：上游供应商根密钥仅存在于 LiteLLM 容器环境变量中。每个租户仅获得带动态模型白名单约束的虚拟密钥（`sk-tenant-*`），并写入租户名下的托管实例 `OpenAI-API-Compatible` → `platform`。
3. **日志脱敏与吊销延迟收敛**：`litellm-config.yaml` 默认启用 `redact_user_api_key_info: true`、`drop_params: true`、`set_verbose: false`、`json_logs: true`，并通过 `user_api_key_cache_ttl: 5` 将网关工作进程内存缓存的密钥吊销延迟控制在 5 秒以内。
4. **后台 Worker 自动托管**：当 RAGFlow API 服务（`ServerTypeAPI`）在配置了 `LITELLM_BASE_URL` 与 `LITELLM_MASTER_KEY` 的环境下启动时，会自动拉起 5 个后台协同任务（`backend/worker/worker.go`）：
   - **自愈租户协调器（Self-Healing Tenant Reconciler）**：两级重试机制，确保新建或降级租户完成虚拟密钥与默认模型初始化。
   - **模型下发 Worker（Publish Rollout Worker）**：在上架、退役、重新启用、设为默认时异步向所有租户收敛。
   - **计量守护进程（Metering Daemon）**：基于水位线的增量账单采集、按日汇总与可选原始日志保留清理。
   - **滞留旧密钥清扫器（`sweepDirtyKeys`）**：每 `1m` 扫描一次超过 `2m` 仍未完成旧密钥吊销的租户并自动重试。
   - **孤儿租户扫描器（`scanOrphanedTenants`）**：每 `1h` 扫描已从上游删除的租户并自动回收其网关虚拟密钥。

---

## 2. 生产部署与初始化配置

### 2.1 配置并启动模型网关栈

网关部署文件位于 `extensions/model-platform/gateway/`：

- `docker-compose.yml`：锁定 `postgres:16-alpine` 与 `ghcr.io/berriai/litellm:v1.110.0`，运行在独立桥接网络 `ragflow-model-platform-net` 中并附带健康探针。
- `litellm-config.yaml`：默认 `model_list: []`，避免在私有化或离线部署中引入不可达的公网模型。
- `validate-gateway-config.sh`：安全与配置预检脚本。

**第一步 — 准备 `.env` 文件：**

```sh
cd extensions/model-platform/gateway
cp .env.example .env
```

在 `.env` 中配置高强度唯一密钥：

| 环境变量 | 必填 | 说明 |
| :--- | :---: | :--- |
| `POSTGRES_DB` | 否 | LiteLLM 专用数据库名（默认 `litellm`）。 |
| `POSTGRES_USER` | 否 | LiteLLM 专用数据库用户名（默认 `litellm`）。 |
| `POSTGRES_PASSWORD` | 是 | LiteLLM PostgreSQL 强密码。 |
| `DATABASE_URL` | 否 | 完整 PostgreSQL DSN；留空时由 `POSTGRES_*` 自动拼装。 |
| `LITELLM_MASTER_KEY` | 是 | LiteLLM 管理接口主密钥（如 `sk-master-...`），需与 RAGFlow API/Admin 进程保持一致。 |
| `LITELLM_SALT_KEY` | 是 | LiteLLM 数据库密文字段加密盐。**首次启动后切勿随意变更**，否则将导致已加密记录无法解密。 |
| `PLATFORM_UPSTREAM_OPENAI_KEY` | 是 | 上游根供应商密钥，在 `litellm-config.yaml` 中通过 `os.environ/...` 引用。如需接入其他供应商，可按需追加 `PLATFORM_UPSTREAM_*` 变量。 |
| `PLATFORM_UPSTREAM_OPENAI_BASE` | 否 | 上游 API 基础地址（默认 `https://api.openai.com/v1`）。 |

**第二步 — 在 `litellm-config.yaml` 中添加模型路由：**

任何准备在 RAGFlow 中上架的模型，都必须先在 LiteLLM 中配置路由并可被 `GET /v1/models` 返回。示例：

```yaml
model_list:
  - model_name: gpt-4o-mini
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/PLATFORM_UPSTREAM_OPENAI_KEY
      api_base: os.environ/PLATFORM_UPSTREAM_OPENAI_BASE
  - model_name: text-embedding-3-small
    litellm_params:
      model: openai/text-embedding-3-small
      api_key: os.environ/PLATFORM_UPSTREAM_OPENAI_KEY
      api_base: os.environ/PLATFORM_UPSTREAM_OPENAI_BASE
```

> [!CAUTION]
> 严禁在 `litellm-config.yaml` 中硬编码任何明文 API Key，必须统一使用 `os.environ/<变量名>` 引用环境变量。

**第三步 — 校验配置并启动：**

```sh
./validate-gateway-config.sh
docker compose up -d
docker compose ps
```

在宿主机本地验证环回健康状态：

```sh
curl -fsS http://127.0.0.1:4000/health/liveliness
```

### 2.2 构建并配置 RAGFlow 服务

组装企业扩展叠加工作区（`build/ragflow`）：

```sh
./extensions/model-platform/scripts/apply-overlay.sh
```

在启动增强版 RAGFlow 服务时，请同时为 **API 服务**（运行后台 Worker）与 **Admin 服务**（提供 `/api/v1/admin/model-platform/*`）注入 `LITELLM_BASE_URL` 和 `LITELLM_MASTER_KEY`。

> [!WARNING]
> 若 API 服务未设置 `LITELLM_BASE_URL` 或 `LITELLM_MASTER_KEY`，后台 Worker 不会启动（`ErrGatewayNotConfigured`），新建租户将停留在 `pending` 状态；若 Admin 服务未配置，则 `POST /models` 与 `POST /tenants/:tenant_id/rotate-key` 会返回 `503 Service Unavailable`。

---

## 3. 环境变量配置参考

### 3.1 网关与控制面连接参数（RAGFlow API / Admin 进程）

| 环境变量 | 默认值 | 生效进程 | 说明 |
| :--- | :--- | :--- | :--- |
| `LITELLM_BASE_URL` | *(空)* | API + Admin | LiteLLM 网关内部地址（如 `http://127.0.0.1:4000`）。启用后台 Worker、模型上架、深度健康检查与密钥轮换的必需项。 |
| `LITELLM_MASTER_KEY` | *(空)* | API + Admin | 与 `LITELLM_BASE_URL` 配套的 LiteLLM 主密钥。 |

### 3.2 计量采集与数据保留参数（RAGFlow API 进程）

所有时间间隔均使用 Go `time.ParseDuration` 格式（例如 `30s`、`5m`、`1h`）。

| 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `METERING_DAEMON_ENABLED` | `true` | 设为 `false` 可关闭后台账单采集、每日汇总与过期日志清理任务。 |
| `METERING_POLL_INTERVAL` | `5m` | 从 LiteLLM `/global/spend/logs` 增量拉取原始账单日志的周期。 |
| `METERING_AGGREGATION_INTERVAL` | `1h` | 执行常规日历日聚合写入 `platform_tenant_usage_daily` 的周期。 |
| `METERING_FETCH_OVERLAP` | `5m` | 每次轮询相对水位线向前重叠的安全窗口，用于兜底网关延迟写入（原始表通过 `request_id` 唯一去重，不会重复计费）。 |
| `METERING_LOOKBACK_HOURS` | `48` | 冷启动与常规滚动聚合回看小时数（按 `METERING_TIMEZONE` 对齐到自然日边界）。 |
| `METERING_TIMEZONE` | `UTC` | 业务计费自然日所属时区。支持 `UTC`、`Local`、IANA 时区名（如 `Asia/Shanghai`、`America/New_York`）或数字偏移（如 `+08:00`、`-05:00`）。 |
| `METERING_SPEND_LOG_RETENTION_DAYS` | `0` | 原始账单表 `platform_gateway_spend_log` 的可选保留天数（`0` 表示永久保留）。设为 `> 0` 后，每日清理任务会在**首次启动拉取完成之后**并在聚合互斥锁保护下分批清理过期原始日志，避免影响进行中的历史补齐聚合。 |
| `METERING_COLLECTOR_BATCH_SIZE` | `100` | 原始账单日志单批写入数据库的记录条数。 |
| `METERING_PIPELINE_BATCH_SIZE` | `100` | 每日用量汇总单批 Upsert 的记录条数。 |

> [!TIP]
> 如果同时在 `litellm-config.yaml` 中启用了 `general_settings.maximum_spend_logs_retention_period`，建议将 LiteLLM 侧保留周期设置得**短于** RAGFlow 侧的 `METERING_SPEND_LOG_RETENTION_DAYS`（或保持 RAGFlow 为 `0` 永久保留），以便控制面作为更长周期的权威审计库。

---

## 4. Admin 控制台界面操作指南

使用 **RAGFlow 超级管理员（Superuser）** 账号登录 Admin 控制台（`/admin`）。模型平台扩展在左侧导航栏新增了 3 个管理页面：

- **模型供应商**（`/admin/model-providers`）
- **模型用量统计**（`/admin/model-usage-statistics`）
- **租户凭证与审计**（`/admin/model-tenants`）

### 4.1 模型供应商（`/admin/model-providers`）

![Admin 控制台 — 平台托管模型](assets/zh-CN/admin-model-providers.png)

用于监控网关连通性，并全生命周期管理 7 类平台托管模型（`对话模型`、`向量模型`、`重排模型`、`语音识别 (ASR)`、`视觉模型`、`语音合成 (TTS)`、`决策模型`）。

1. **网关健康状态徽章（右上角）**：每 30 秒自动探测 `GET /api/v1/admin/model-platform/health`，展示 `网关正常 (Nms)`、`网关异常` 或 `网关不可达`，支持点击刷新图标立即探活。
2. **平台模型池概览**：7 张卡片分别汇总各类模型的可用数量与当前平台默认模型。
3. **上架模型**：
   - 前置条件：先在 `gateway/litellm-config.yaml` 添加路由并重载/重启 LiteLLM。
   - 点击 **上架模型**，输入与网关路由一致的 `model_name`（如 `deepseek-v3` 或 `tts-1`），选择模型类型并确认。
   - 后端会校验网关路由存在性、写入平台目录并异步向所有就绪租户分发。**上架新模型不会修改任何现有默认模型设置。**
4. **设为默认（`设为默认`）**：
   - 对任意“已启用”且非默认的模型可执行。
   - 系统将把来源为 `platform_default` 的用户自动迁移到新默认模型，同时**完整保留已手动自定义过默认模型的用户（`user_explicit`）**。
5. **退役（`退役`）与重新启用（`重新启用`）**：
   - 当前某类型的平台默认模型不允许直接退役，需先将同类型其他活跃模型设为默认。
   - 退役前请先核对表格中的 **租户分布（可用 / 只读 / 已退役）** 与 **引用数**（`N 个知识库` / `N 个助手`）：
     - **向量模型（Embedding）** 采用三阶段退役（`已启用` → `只读/弃用` → `已退役`）：仍关联知识库的租户保留只读检索能力，无引用的租户立即移除。
     - **非向量模型**（对话、决策、重排、ASR、视觉、TTS）会立即在所有租户禁用：引用该模型的助手会提示所有者改选其他模型，默认使用该模型的用户会迁移至平台默认模型。
   - 点击已退役模型的 **重新启用** 可将其重新恢复至全部租户。
6. **下发进度横幅（Rollout Banner）**：
   - 触发上架、退役、重新启用或设为默认后自动显示，每 3 秒轮询最新进度。
   - 展示已完成/总租户数、待处理数、失败数，以及（切默认场景下）已迁移用户数与保留自定义选择用户数。
   - 可展开 **失败详情** 分页查看具体租户报错，并点击 **重试失败租户** 一键重新入队。

```mermaid
stateDiagram-v2
    [*] --> Active: 上架模型
    Active --> Active: 设为默认（仅迁移 platform_default 用户）
    Active --> Deprecated: 退役（仍被知识库引用的向量模型）
    Active --> Tombstoned: 退役（非向量模型，或 0 引用的向量模型）
    Deprecated --> Tombstoned: 最后一个关联知识库被删除
    Deprecated --> Active: 重新启用
    Tombstoned --> Active: 重新启用
```

### 4.2 模型用量统计（`/admin/model-usage-statistics`）

![Admin 控制台 — 模型用量统计](assets/zh-CN/admin-model-usage.png)

用于查看平台整体或单租户的消费明细、Token 结构与消费隔离状态。

- **多维筛选栏**：支持按 **模型类型**、**租户 ID**、**模型名称**、**开始日期**、**结束日期**（`YYYY-MM-DD`）组合过滤，支持一键 **重置**。
- **核心 KPI 卡片**：
  1. **累计费用**：展示总费用（USD）、总调用次数及提示词缓存节省金额。
  2. **总 Token 消耗**：拆分展示输入 / 输出 Token 与缓存 / 推理 Token。
  3. **语音合成字符数 (TTS)**：展示通过 `/v1/audio/speech` 计量的合成字符总数。
  4. **消费隔离审计**：在筛选栏填入 **租户 ID** 后点击 **运行审计**，直接基于 `platform_gateway_spend_log` 核验该租户是否存在非托管/影子模型费用。如检测到违规，将弹出醒目告警横幅并列出异常模型。
- **每日消耗趋势**：支持在 **费用 (USD)** 与 **Token 数** 折线之间切换。当匹配明细超过 100 条时，页面会提示当前为部分统计，建议缩小日期范围查看完整趋势。
- **每日用量明细**：按 `(日期, 租户, 模型)` 分页展示已对账记录（每页 20 条）。

### 4.3 租户凭证与审计（`/admin/model-tenants`）

![Admin 控制台 — 租户凭证与审计](assets/zh-CN/admin-tenant-credentials.png)

用于巡检各托管租户的虚拟密钥状态、触发单租户应急轮换以及执行消费隔离审计。

- **同步状态筛选与游标翻页**：支持按 `就绪`、`降级`、`失败`、`待处理`、`初始化中`、`已删除` 6 种状态筛选；列表采用稳定游标分页（`after`），避免状态变动导致翻页漏行。
- **密钥元数据（绝不暴露明文）**：仅展示 `密钥别名`、8 位 SHA-256 `密钥指纹`、`最近轮换` 时间/操作人以及 `密钥状态`（`已收敛` / `待完成吊销`），API 与界面均永不返回明文密钥。
- **轮换密钥 / 重新轮换并吊销**：
  - 仅 `就绪` 状态的租户允许触发。
  - 执行时先签发新虚拟密钥并原子更新租户 `platform` 实例（零业务中断），随后立即吊销旧密钥。
  - 若上次轮换时旧密钥吊销尚未完成（`密钥状态 = 待完成吊销`），按钮显示为 **重新轮换并吊销**，点击即可立即补全吊销（后台清扫任务也会自动重试）。
- **运行审计**：弹出 **租户消费隔离审计** 对话框，对比平台托管费用与非托管/影子消费并展示违规明细。

---

## 5. Admin API 速查与批量运维工具

全部 13 个管理端点均挂载于 `/api/v1/admin/model-platform` 下，需携带 Superuser `Authorization` 请求头（完整参数与 JSON 示例见 [API 参考文档](api-reference.zh-CN.md)）。

| 方法与路径 | 用途 |
| :--- | :--- |
| `GET /health` | 管理服务与下游 LiteLLM 网关深度健康检查 |
| `GET /models` | 查询托管模型目录、租户分布状态及知识库/助手引用数 |
| `POST /models` | 上架已在网关配置路由的模型并启动全租户下发（`202`） |
| `POST /models/:id/retire` | 退役非默认托管模型（`202`） |
| `POST /models/:id/reactivate` | 重新启用已退役托管模型（`202`） |
| `PUT /models/:id/default` | 将活跃模型设为该类型的平台默认模型（`202`） |
| `GET /models/publish/tasks/:id` | 查询下发任务进度及分页失败租户详情 |
| `POST /models/publish/tasks/:id/retry` | 将任务中所有失败租户重置为待重试状态 |
| `GET /tenants` | 游标分页查询托管租户及其密钥状态 |
| `POST /tenants/:tenant_id/rotate-key` | 单租户零中断虚拟密钥轮换（`200` / `202`） |
| `GET /usage/daily` | 多维筛选分页查询每日用量明细 |
| `GET /usage/summary` | 查询聚合消费汇总（含 `by_model_type` 分类型统计） |
| `GET /usage/audit` | 实时执行单租户消费隔离审计（`tenant_id` 必填） |

### 应急批量密钥轮换脚本

遇到密钥疑似泄露或例行全量轮换时，可使用 `extensions/model-platform/scripts/emergency-rotate-keys.sh`（依赖 `curl`、`jq`，以及通过 `POST /api/v1/admin/login` 获取的 Superuser `ADMIN_TOKEN`）：

```sh
# 1. 干跑预览将被轮换的租户清单，不做任何变更
ADMIN_TOKEN="<superuser-token>" \
ADMIN_URL="http://127.0.0.1:9381/api/v1/admin" \
./extensions/model-platform/scripts/emergency-rotate-keys.sh --all --dry-run

# 2. 轮换单个租户
ADMIN_TOKEN="<superuser-token>" \
./extensions/model-platform/scripts/emergency-rotate-keys.sh --tenant tenant_acme_01

# 3. 限制并发与速率，批量轮换全部就绪租户
ADMIN_TOKEN="<superuser-token>" \
./extensions/model-platform/scripts/emergency-rotate-keys.sh \
  --all --force --concurrency 10 --rate-limit 5 \
  --output ./rotate-keys.ndjson

# 4. 排查异常后断点续跑（自动跳过已返回 HTTP 200 的租户）
ADMIN_TOKEN="<superuser-token>" \
./extensions/model-platform/scripts/emergency-rotate-keys.sh \
  --all --force --resume ./rotate-keys.ndjson --output ./rotate-keys.ndjson
```

**退出码说明**：`0` = 目标租户全部轮换完成且旧密钥吊销成功（`200`）；`1` = 存在失败租户或返回 `202`（旧密钥吊销仍在后台收敛）；`2` = 参数非法或缺少必要依赖。

---

## 6. 故障排查与容灾演练手册

### 6.1 网关宕机与租户初始化自愈

```mermaid
flowchart TD
    Create["新建租户"] --> Hook["数据库事务钩子：写入 platform_tenant_reconcile (status=pending)"]
    Hook --> PreFlight["事务提交后 PreFlightCheck -> EventBus"]
    PreFlight --> Tier1["一级协调：内存快速指数退避重试"]
    Tier1 -->|"成功"| Ready["status = ready"]
    Tier1 -->|"网关不可达"| Degraded["status = degraded（写入 next_retry_at）"]
    Degraded --> Tier2["二级协调：后台 Poller 定时自愈巡检"]
    Tier2 -->|"网关恢复"| Ready
```

- **故障期间的表现**：租户注册仍可在毫秒级完成，因为 `OnTenantCreateHook` 在事务内仅执行本地 SQL，绝不发起外部 RPC；受影响租户暂处于 `pending` 或 `degraded` 状态。
- **恢复步骤**：
  1. 恢复 LiteLLM 与 PostgreSQL（在 `extensions/model-platform/gateway` 下执行 `docker compose up -d`）。
  2. 确认 `GET /api/v1/admin/model-platform/health` 返回 `200` 且 `"gateway": "healthy"`。
  3. 在 **Admin 控制台 → 租户凭证与审计** 中筛选 `降级` / `待处理` / `失败`，确认二级 Poller 已将其自动收敛回 `就绪`。

### 6.2 计量中断与历史账单自愈补齐

- **正常在线运行**：`SpendLogCollector` 使用内存水位线配合 `METERING_FETCH_OVERLAP`（默认 `5m`）执行小窗口增量拉取，长时空闲也不会产生超大范围扫描。
- **API 服务或网关长时间中断后恢复**：
  - 服务重启后，采集器从数据库持久水位线起无上限回放拉取（首次冷启动按 `METERING_LOOKBACK_HOURS` 自然日对齐），每条原始记录按 `request_id` 幂等去重（`ON CONFLICT DO NOTHING`）。
  - 一旦补齐写入了早于常规回看窗口的历史账单，`MeteringDaemon` 会自动触发**动态多日 Catch-up 聚合**，以**确定性替换写入（Replacement Upsert，绝不使用 `+=` 累加）**重算对应自然日数据，保证零重复计费。
- **核验方式**：等待一个 `METERING_POLL_INTERVAL` + `METERING_AGGREGATION_INTERVAL` 周期后，查询 `GET /api/v1/admin/model-platform/usage/daily?start_date=...&end_date=...`，核对断档日期的数据已补齐。

### 6.3 密钥轮换中途中断（`待完成吊销`）

如果签发新虚拟密钥并写入 RAGFlow 数据库后网关恰好失联，租户业务保持零中断，但协调记录会被打上 `key_dirty_since` 标记：

- **自动清扫**：后台 `sweepDirtyKeys` 每 `1m` 自动巡检超过 `2m` 未收敛的租户并重试吊销旧密钥。
- **人工立即收敛**：在 **租户凭证与审计** 页面找到状态为 **待完成吊销** 的租户，点击 **重新轮换并吊销**（或再次调用 `POST /tenants/:tenant_id/rotate-key`）。

### 6.4 影子消费 / 隔离违规告警排查

当 **消费隔离审计** 返回 `audit_passed: false` 时：

1. 在审计弹窗或 `GET /usage/audit?tenant_id=<id>` 响应中查看 `unmanaged_models_detected` 与 `violations`。
2. 核实是否有人手动在 LiteLLM 中给该租户虚拟密钥放开了未上架模型，或手工删除了数据库模型记录而未同步收缩网关白名单。
3. 执行单租户密钥轮换（`POST /tenants/:tenant_id/rotate-key`），系统会根据租户当前的活跃/只读托管模型重新生成严格白名单并吊销旧密钥；完成后重新点击 **运行审计** 确认恢复为 **隔离正常**。
