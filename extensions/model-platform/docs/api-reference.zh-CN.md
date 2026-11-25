# 模型平台 API 参考文档

[English](api-reference.md) | 简体中文

本文档完整列出 `model-platform` 扩展在 Admin 服务与 App API 服务两端新增或施加约束的全部 HTTP 接口契约：

- **第一部分 — Admin 服务 API**（默认端口 `9381`，路径前缀 `/api/v1/admin/model-platform`）：共 **13 个新增管理接口**，覆盖网关健康检查、托管模型目录与全租户发布治理、租户密钥应急轮换以及用量计量与消费隔离审计。
- **第二部分 — App API 服务守卫契约**（默认端口 `9380`，路径前缀 `/api/v1`）：不新增独立的用户侧路径，而是在上游受保护路由组挂载 **3 个保护/观测中间件**，对既有供应商、知识库与默认模型接口强制执行平台不变式。

---

## 通用约定

### 鉴权方式

| 服务 | 默认基础路径 | 鉴权方式 |
| :--- | :--- | :--- |
| **Admin 服务** | `http://127.0.0.1:9381/api/v1/admin/model-platform` | 通过 `POST /api/v1/admin/login` 使用 Superuser 账号登录，从响应头 `Authorization` 获取 Token，并在后续请求的 `Authorization` 头中携带。非 Superuser 登录返回 `403`。 |
| **App API 服务** | `http://127.0.0.1:9380/api/v1` | 标准 RAGFlow 用户会话 Token 或 API Key（`Authorization` 请求头）。 |

### 响应信封格式

除 **`GET /health`** 外，所有 Admin 接口均返回统一 JSON 信封：

```json
{
  "code": 200,
  "data": {}
}
```

错误响应格式：

```json
{
  "code": 400,
  "message": "错误描述信息"
}
```

`GET /health` 返回顶层扁平 JSON 对象，无 `data` 包裹（见 1.1 节）。

### `model_type` 位标志枚举

定义于 `extensions/model-platform/backend/entity/model_slots.go`（前后端唯一真值）。本地 `ocr`（`64`）不走 LiteLLM 网关，因此不在托管范围内。

| `model_type` | 标识键 | `user_tenant` 槽位字段 | 计量单位 | 引用统计对象 |
| :---: | :--- | :--- | :--- | :--- |
| `1` | `chat` | `llm_id` | Tokens | 助手（`dialog`） |
| `2` | `embedding` | `embd_id` | Tokens | 知识库（`knowledgebase`） |
| `4` | `asr` | `asr_id` | Tokens | — |
| `8` | `vision` | `img2txt_id` | Tokens | — |
| `16` | `rerank` | `rerank_id` | Tokens | — |
| `32` | `tts` | `tts_id` | 字符数（Characters） | — |
| `128` | `decision` | `decision_id` | Tokens | 助手（`dialog`） |

---

## 第一部分：Admin 服务 API（`/api/v1/admin/model-platform`）

### 1.1 健康检查探针

#### `GET /api/v1/admin/model-platform/health`

检查模型平台管理服务状态；当配置了 `LITELLM_BASE_URL` 时，同时向下游 LiteLLM 网关发起主动连通性探测。

**响应示例：**

- `200 OK`（网关健康或未配置网关）：

  ```json
  {
    "status": "ok",
    "service": "model-platform-admin",
    "gateway": "healthy"
  }
  ```

- `503 Service Unavailable`（网关不可达）：

  ```json
  {
    "status": "degraded",
    "service": "model-platform-admin",
    "gateway": "unreachable",
    "error": "dial tcp 127.0.0.1:4000: connect: connection refused"
  }
  ```

---

### 1.2 托管模型目录与下发任务

#### `GET /api/v1/admin/model-platform/models`

按 `model_name` 升序返回全部平台目录模型，并附带各模型在托管 `platform` 实例上的租户状态分布（`active`、`deprecated`、`tombstoned`）及活跃知识库/助手引用计数（不计入租户自有 BYOK 模型）。

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": [
    {
      "id": "mdl-emb3small",
      "model_name": "text-embedding-3-small",
      "model_type": 2,
      "is_default": true,
      "status": "active",
      "tenants": {
        "active": 42,
        "deprecated": 0,
        "tombstoned": 0
      },
      "active_kb_count": 96,
      "active_dialog_count": 0,
      "create_time": 1762000000000
    }
  ]
}
```

---

#### `POST /api/v1/admin/model-platform/models`

将模型录入平台托管目录，并启动异步任务向所有尚未拥有该模型的 `ready` 租户下发。对已上架的相同 `(model_name, model_type)` 重复调用具有幂等性，仅补发缺失租户。**绝不修改目录级的平台默认模型**；但若管理员对某个已被设为平台默认模型的条目重新执行上架以补发缺失租户，下发 Worker 在为这些租户补齐该模型后，会将其中来源为 `platform_default` 或尚无偏好记录的用户迁移至该默认模型（保留 `user_explicit` 用户）。

**请求体字段：**

| 字段 | 类型 | 必填 | 约束 |
| :--- | :--- | :---: | :--- |
| `model_name` | `string` | 是 | 需匹配正则 `^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`，且已在网关 `GET /v1/models` 列表中可路由。 |
| `model_type` | `int` | 是 | 必须为 `1, 2, 4, 8, 16, 32, 128` 之一。 |

```sh
curl -X POST http://127.0.0.1:9381/api/v1/admin/model-platform/models \
  -H "Authorization: ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"model_name":"deepseek-v3","model_type":1}'
```

**响应示例（`202 Accepted`）：**

```json
{
  "code": 202,
  "data": {
    "task_id": "8c2f4f209c1a48a9b7b0e4c1d2e3f4a5",
    "total": 42
  }
}
```

**错误码：**

| HTTP 状态码 | 说明 |
| :---: | :--- |
| `400` | JSON 格式错误、`model_name` 不符合正则或 `model_type` 不受支持。 |
| `409` | 目录冲突（如同名模型已以其他类型或退役状态存在）。 |
| `422` | LiteLLM 网关当前尚未配置该模型的路由。 |
| `502` | 校验网关路由时网关不可达。 |
| `503` | 网关（`LITELLM_BASE_URL`）或数据库未配置。 |

---

#### `POST /api/v1/admin/model-platform/models/:id/retire`

退役一个活跃的托管模型并启动异步发布任务，由后台发布 Worker 按租户逐个收敛：

- **向量模型（`2`）**：存在知识库引用的租户在任务处理到该租户时转为 `deprecated`（只读兼容），无引用的租户异步进入 `tombstoned`。
- **非向量模型（`1, 4, 8, 16, 32, 128`）**：由后台 Worker 按租户异步收敛至禁用/`tombstoned`，并将受影响的默认模型用户迁移至当前平台默认模型。

单租户若遇网关或数据库瞬时故障会按退避策略自动重试；达到重试上限（默认 20 次）的条目将转为 `failed`，可通过 `GET /api/v1/admin/model-platform/models/publish/tasks/:id` 轮询查看进度与失败明细，并通过 `POST /api/v1/admin/model-platform/models/publish/tasks/:id/retry` 重新入队。

**响应（`202 Accepted`）：** 返回 `task_id`、`total` 与可选的 `affected_users_count`。

**错误码：**

| HTTP 状态码 | 说明 |
| :---: | :--- |
| `400` | 尝试退役当前某类型的平台默认模型（请先切换默认模型）。 |
| `404` | 未找到目录模型 `:id`。 |

---

#### `POST /api/v1/admin/model-platform/models/:id/reactivate`

重新启用一个已退役的托管模型，并启动异步任务将其恢复到全部就绪租户。

**响应（`202 Accepted`）：** `{"code": 202, "data": {"task_id": "...", "total": 42}}`。

**错误码：** `404`（模型不存在）、`409`（目录状态冲突）。

---

#### `PUT /api/v1/admin/model-platform/models/:id/default`

将一个活跃托管模型设为所属 `model_type` 的平台默认模型。后台任务会将来源为 `platform_default` **或尚无偏好记录**（即使其当前默认槽位非空）的用户自动迁移至该模型，同时保留来源为 `user_explicit` 的用户自定义选择。

**响应示例（`202 Accepted`）：**

```json
{
  "code": 202,
  "data": {
    "task_id": "3a9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d",
    "total": 42
  }
}
```

**错误码：** `404`（模型不存在）、`409`（模型非活跃/已退役）。

---

#### `GET /api/v1/admin/model-platform/models/publish/tasks/:id`

查询下发任务 `:id` 的整体进度及重试耗尽的失败租户分页明细。

**查询参数：**

| 参数 | 类型 | 默认值 | 取值范围 |
| :--- | :--- | :---: | :--- |
| `page` | `int` | `1` | `>= 1` |
| `page_size` | `int` | `20` | `1..100` |

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": {
    "progress": {
      "task_id": "task-rollout-20261125",
      "model_name": "deepseek-v3",
      "model_type": 1,
      "status": "partial_failed",
      "total": 42,
      "succeeded": 41,
      "pending": 0,
      "failed": 1,
      "upgraded_users_count": 28,
      "preserved_users_count": 13,
      "created_at": "2026-11-25T08:15:00Z"
    },
    "failed_items": {
      "total": 1,
      "page": 1,
      "page_size": 20,
      "items": [
        {
          "tenant_id": "tenant_east_09",
          "retry_count": 5,
          "last_error": "gateway key update timeout: context deadline exceeded"
        }
      ]
    }
  }
}
```

`progress.status` 取值：`running`（`pending > 0`）、`partial_failed`（`pending == 0 && failed > 0`）、`completed`（`pending == 0 && failed == 0`）。

**错误码：** `404`（任务不存在）。

---

#### `POST /api/v1/admin/model-platform/models/publish/tasks/:id/retry`

将任务 `:id` 下所有 `failed` 状态的租户重置为 `pending`（`retry_count = 0`、`next_retry_at = NULL`），供后台 Worker 立即重试。

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": {
    "retried": 1
  }
}
```

**错误码：** `404`（任务不存在）。

---

### 1.3 租户凭证查询与应急密钥轮换

#### `GET /api/v1/admin/model-platform/tenants`

采用游标分页列出托管租户及其虚拟密钥元数据。**接口永不返回明文密钥。**

**查询参数：**

| 参数 | 类型 | 默认值 | 说明 |
| :--- | :--- | :---: | :--- |
| `status` | `string` | `ready` | 同步状态筛选：`ready`、`pending`、`provisioning`、`degraded`、`failed`、`deleted`。 |
| `after` | `string` | *(空)* | 上一页最后一条记录的 `tenant_id`（开区间游标）。 |
| `page_size` | `int` | `100` | 每页条数（`1..1000`）。 |

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": {
    "total": 42,
    "page_size": 100,
    "items": [
      {
        "tenant_id": "tenant_acme_01",
        "status": "ready",
        "key_alias": "tenant_tenant_acme_01_a94f3c1e",
        "key_fingerprint": "a94f3c1e",
        "key_rotated_at": "2026-11-24T14:22:10Z",
        "key_rotated_by": "admin@example.com"
      },
      {
        "tenant_id": "tenant_nova_02",
        "status": "ready",
        "key_alias": "tenant_tenant_nova_02_7d12e8b4",
        "key_fingerprint": "7d12e8b4",
        "key_rotated_at": "2026-11-25T07:50:00Z",
        "key_rotated_by": "admin@example.com",
        "key_dirty_since": "2026-11-25T07:50:05Z"
      }
    ]
  }
}
```

- `key_fingerprint`：虚拟密钥 SHA-256 摘要的前 8 位十六进制字符，仅用于区分密钥版本。
- `key_dirty_since`：仅在存在尚未完成吊销的旧密钥时出现。

---

#### `POST /api/v1/admin/model-platform/tenants/:tenant_id/rotate-key`

为指定租户签发新的 LiteLLM 虚拟密钥、原子更新租户的托管 `platform` 实例（零业务中断）、记录操作人审计信息并吊销旧密钥。

```sh
curl -X POST http://127.0.0.1:9381/api/v1/admin/model-platform/tenants/tenant_acme_01/rotate-key \
  -H "Authorization: ${ADMIN_TOKEN}"
```

**成功响应：**

- `200 OK` — 新密钥已生效，且**全部旧密钥已完成吊销**（`old_keys_revoked: true`）。
- `202 Accepted` — 新密钥已生效，但**部分旧密钥吊销仍在后台收敛中**（`old_keys_revoked: false`）。等待后台 `sweepDirtyKeys` 仅重试吊销旧密钥；若再次调用本接口，则会执行一次新的完整轮换（先签发并切换到另一把新密钥，再重试吊销包括前一次在内的所有旧密钥，且若网关仍异常并不保证吊销成功）。

```json
{
  "code": 200,
  "data": {
    "tenant_id": "tenant_acme_01",
    "key_alias": "tenant_tenant_acme_01_9c31b7d2",
    "new_key_fingerprint": "9c31b7d2",
    "rotated_at": "2026-11-25T09:10:00Z",
    "revoked_keys": 1,
    "old_keys_revoked": true
  }
}
```

**错误码：**

| HTTP 状态码 | 说明 |
| :---: | :--- |
| `400` | `:tenant_id` 非法。 |
| `401` | 上下文中缺少已认证管理员身份（`user_id` / `email`）。 |
| `404` | 该租户不由模型平台托管。 |
| `409` | 租户当前非 `ready` 状态，或该租户已有进行中的轮换任务。 |
| `502` | 切换前网关不可达，原有效密钥保持不变。 |
| `503` | 网关或数据库未配置。 |

---

### 1.4 用量计量与消费隔离审计

#### `GET /api/v1/admin/model-platform/usage/daily`

分页查询 `platform_tenant_usage_daily` 中的每日聚合消费明细。

**查询参数：**

| 参数 | 类型 | 默认值 | 说明 |
| :--- | :--- | :---: | :--- |
| `tenant_id` | `string` | *(全部)* | 精确租户 ID 过滤。 |
| `model_name` | `string` | *(全部)* | 精确模型名称过滤。 |
| `model_type` | `int` | *(全部)* | 必须为 `1, 2, 4, 8, 16, 32, 128` 之一（非法值返回 `400`）。 |
| `start_date` | `string` | *(不限)* | 起始日期（含，`YYYY-MM-DD`）。 |
| `end_date` | `string` | *(不限)* | 结束日期（含，`YYYY-MM-DD`）。 |
| `page` | `int` | `1` | `>= 1`。 |
| `page_size` | `int` | `20` | `1..100`。 |

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": {
    "total": 1,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "tenant_id": "tenant_acme_01",
        "model_name": "gpt-4o-mini",
        "model_type": 1,
        "usage_date": "2026-11-25",
        "call_count": 1420,
        "prompt_tokens": 1280000,
        "completion_tokens": 410000,
        "total_tokens": 1690000,
        "cached_tokens": 340000,
        "cache_write_tokens": 22000,
        "reasoning_tokens": 45000,
        "characters": 0,
        "total_spend": 0.4382,
        "prompt_spend": 0.192,
        "completion_spend": 0.2462,
        "cache_savings": 0.051,
        "created_at": "2026-11-25T01:00:00Z",
        "updated_at": "2026-11-25T09:00:00Z"
      }
    ]
  }
}
```

---

#### `GET /api/v1/admin/model-platform/usage/summary`

使用与 `/usage/daily` 相同的筛选参数（`tenant_id`、`model_name`、`model_type`、`start_date`、`end_date`），返回单租户或全平台的聚合消费汇总，包含分模型汇总与 `by_model_type` 分类型明细。

**响应示例（`200 OK`）：**

```json
{
  "code": 200,
  "data": {
    "tenant_id": "tenant_acme_01",
    "total_calls": 2060,
    "total_prompt_tokens": 3430000,
    "total_completion_tokens": 410000,
    "total_tokens": 3840000,
    "total_cached_tokens": 340000,
    "total_cache_write_tokens": 22000,
    "total_reasoning_tokens": 45000,
    "total_characters": 0,
    "total_spend": 0.4812,
    "total_prompt_spend": 0.235,
    "total_completion_spend": 0.2462,
    "total_cache_savings": 0.051,
    "model_tokens": {
      "gpt-4o-mini": 1690000,
      "text-embedding-3-small": 2150000
    },
    "by_model_type": {
      "chat": {
        "model_type": 1,
        "call_count": 1420,
        "prompt_tokens": 1280000,
        "completion_tokens": 410000,
        "total_tokens": 1690000,
        "characters": 0,
        "total_spend": 0.4382
      },
      "embedding": {
        "model_type": 2,
        "call_count": 640,
        "prompt_tokens": 2150000,
        "completion_tokens": 0,
        "total_tokens": 2150000,
        "characters": 0,
        "total_spend": 0.043
      }
    },
    "start_date": "2026-11-25",
    "end_date": "2026-11-25"
  }
}
```

---

#### `GET /api/v1/admin/model-platform/usage/audit`

直接扫描当前留存在权威原始账单暂存表 `platform_gateway_spend_log` 中的记录，将指定 `tenant_id` 的每一条已采集 `model_name` 与该租户在 `OpenAI-API-Compatible` → `platform` 下当前的 `tenant_model` 清单逐条做精确字符串比对（不按模型状态过滤）。审计通过（`audit_passed: true`）仅表示当前暂存表中的所有网关计费记录均命中该托管模型清单：它不覆盖绕过网关、尚未采集或已因保留策略被清理的流量，也不证明每条历史调用在发生时均处于已授权状态（例如已禁用或已退役但仍保留在 `tenant_model` 中的同名模型也会计入 `managed_spend_total`）。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :---: | :--- |
| `tenant_id` | `string` | 是 | 待审计租户 ID（缺失或为空返回 `400`）。 |

```sh
curl "http://127.0.0.1:9381/api/v1/admin/model-platform/usage/audit?tenant_id=tenant_acme_01" \
  -H "Authorization: ${ADMIN_TOKEN}"
```

**响应示例（`200 OK` — 隔离正常）：**

```json
{
  "code": 200,
  "data": {
    "tenant_id": "tenant_acme_01",
    "managed_spend_total": 1.4077,
    "self_owned_spend_total": 0,
    "audit_passed": true
  }
}
```

**响应示例（`200 OK` — 检测到异常/影子消费）：**

```json
{
  "code": 200,
  "data": {
    "tenant_id": "tenant_acme_01",
    "managed_spend_total": 1.4077,
    "self_owned_spend_total": 0.0842,
    "unmanaged_models_detected": ["unapproved-shadow-model"],
    "audit_passed": false,
    "violations": [
      "unmanaged model \"unapproved-shadow-model\" found in raw gateway logs: spend=0.084200"
    ]
  }
}
```

---

## 第二部分：App API 服务守卫契约（`/api/v1`）

在 `extensions/model-platform/backend/router/app_routes.go` 中，本扩展**不新增任何独立的用户侧 HTTP 路由**，而是在上游受保护的路由组上挂载了 3 个核心中间件（`RegisterModelPlatformProtectedRoutes`），用于治理既有 RAGFlow 接口行为。

### 2.1 `ManagedProviderGuardMiddleware`

保护平台托管的供应商、`platform` 实例及其下属模型不被租户篡改，同时放行全部只读请求（`GET`、连通性测试）以及租户自有 BYOK 实例的全部写操作。

- **拦截路由与方法**：
  - 针对 `/api/v1/providers/*`、`/setting/model/provider/*`、`/admin/model/provider/*` 的 `PUT`、`PATCH`、`DELETE`（排除 `/dataset` 与 `/artifacts` 路径）。
  - 针对 `.../instances`（创建实例）与 `.../models`（在实例下新增模型）的 `POST`。
- **拦截规则与响应**：

| 触发条件 | HTTP 状态码 | 响应体 |
| :--- | :---: | :--- |
| 修改/删除托管供应商、`platform` 实例（按名称或 ID）或其下属模型；尝试创建或重命名任意实例为 `platform`（不区分大小写）；或在受保护写接口中传入非法/含尾部脏数据的 JSON | `403` | `{"code": 403, "message": "Platform-managed providers and models cannot be modified or deleted by tenants."}` |
| 批量删除实例（`DELETE .../instances`）请求体中包含重复的 `instances` JSON 键 | `400` | `{"code": 400, "message": "Duplicate instances key in request body"}` |
| 批量删除实例目标数量超过 `100` 个 | `400` | `{"code": 400, "message": "Batch instance targets exceed limit"}` |
| 写请求 JSON 体积超过 `MAX_JSON_CONTENT_LENGTH`（默认 `1 MiB`） | `413` | `{"code": 413, "message": "Request entity too large"}` |

---

### 2.2 `DatasetImmutabilityGuardMiddleware`

在知识库创建与更新时防止向量空间损坏及绑定已退役向量模型。检测请求体中的 `embd_id`、`embedding_model`、`tenant_embd_id` 与 `embedding_id` 字段。

- **拦截路由与方法**：
  - `POST /api/v1/datasets`（创建知识库）
  - `PUT /api/v1/datasets/:dataset_id` 与 `PATCH /api/v1/datasets/:dataset_id`（更新知识库）
- **拦截规则与响应**：

| 触发条件 | HTTP 状态码 | 精确错误消息模板 |
| :--- | :---: | :--- |
| 创建知识库时绑定已退役/弃用/只读的向量模型，或更新知识库时尝试切换为另一个已退役/弃用/只读的向量模型 | `400` | `Embedding model "<model>" is retired/deprecated and cannot be used for knowledgebases. Please choose an active model.` |
| 创建知识库未指定向量模型，而当前用户的默认向量模型已退役/弃用 | `400` | `Tenant default embedding model "<model>" is retired/deprecated. Please specify an active embedding model or update tenant default setting.` |
| 对已包含 `chunk_num > 0`、`doc_num > 0` 或存在有效 `document` 记录的知识库尝试更换为另一个**活跃**向量模型（若目标模型本身已退役/弃用，则优先触发上一行的已退役模型错误） | `400` | `Cannot modify embedding model of a dataset that already contains parsed documents or chunks. Create a new dataset to use a different embedding model.` |

> [!NOTE]
> 提交与知识库**当前绑定一致**的向量模型（无变更）始终放行；对**完全为空**的知识库更换为**活跃**向量模型也始终放行。

---

### 2.3 `DefaultModelPreferenceMiddleware`

观测用户主动修改个人默认模型槽位的请求，确保 `StickyGuard` 在未来平台切换全局默认模型时保护用户的显式选择。

- **观测路由与方法**：
  - `PATCH /api/v1/models`
  - `PATCH /api/v1/models/default`
  - `PATCH /api/v1/users/me/models`
- **行为说明**：
  - 不改变上游请求/响应格式，也不引入新的错误码。
  - 在上游处理器执行前后对比当前用户的 `user_tenant` 7 类默认槽位。
  - 当某个槽位变更为新的非空模型，且处理器执行前后的两次默认槽位读取以及写入偏好记录全部成功时，在行锁保护下将其来源记录为 `user_explicit`，使后续 `PUT /api/v1/admin/model-platform/models/:id/default` 滚动升级不会覆盖该用户的选择。若任一步骤失败，则不会写入 `user_explicit` 记录，错误仅附加到请求上下文而不改变响应，后续平台默认升级可能会覆盖该槽位；重复选择当前已选中的同一模型也不会创建记录。
