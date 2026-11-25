# Model Platform API Reference

English | [简体中文](api-reference.zh-CN.md)

This reference documents every HTTP API added or modified by the `model-platform` extension across both services:

- **Part 1 — Admin Service API** (default port `9381`, base path `/api/v1/admin/model-platform`): 13 newly added endpoints for health probing, managed model catalog & rollout governance, tenant credential rotation, and spend metering/auditing.
- **Part 2 — App API Service Guards** (default port `9380`, base path `/api/v1`): No new standalone user endpoints are added; instead, 3 protection/observation middlewares enforce platform invariants on existing upstream provider, dataset, and default-model routes.

---

## Common Conventions

### Authentication

| Service | Base URL (Default) | Authentication |
| :--- | :--- | :--- |
| **Admin Service** | `http://127.0.0.1:9381/api/v1/admin/model-platform` | Superuser token returned in the `Authorization` response header of `POST /api/v1/admin/login`. Pass it back in the `Authorization` request header. Non-superusers are rejected with `403`. |
| **App API Service** | `http://127.0.0.1:9380/api/v1` | Standard RAGFlow user session token or API key (`Authorization` header). |

### Response Envelope

All Admin endpoints **except `/health`** return a standard JSON envelope:

```json
{
  "code": 200,
  "data": {}
}
```

Error responses return:

```json
{
  "code": 400,
  "message": "human-readable error description"
}
```

`GET /health` returns a flat top-level JSON object without a `data` wrapper (see Section 1.1).

### `model_type` Bit-Flag Enumeration

Defined in `extensions/model-platform/backend/entity/model_slots.go` (single source of truth). Local `ocr` (`64`) is intentionally excluded because it runs outside the LiteLLM gateway.

| `model_type` | Key | User Slot Column | Billing Unit | Reference Kind |
| :---: | :--- | :--- | :--- | :--- |
| `1` | `chat` | `llm_id` | Tokens | Assistants (`dialog`) |
| `2` | `embedding` | `embd_id` | Tokens | Datasets (`knowledgebase`) |
| `4` | `asr` | `asr_id` | Tokens | — |
| `8` | `vision` | `img2txt_id` | Tokens | — |
| `16` | `rerank` | `rerank_id` | Tokens | — |
| `32` | `tts` | `tts_id` | Characters | — |
| `128` | `decision` | `decision_id` | Tokens | Assistants (`dialog`) |

---

## Part 1: Admin Service API (`/api/v1/admin/model-platform`)

### 1.1 Health Probe

#### `GET /api/v1/admin/model-platform/health`

Checks the model platform admin service and, when `LITELLM_BASE_URL` is configured, actively probes the downstream LiteLLM gateway.

**Responses:**

- `200 OK` (gateway healthy or not configured):

  ```json
  {
    "status": "ok",
    "service": "model-platform-admin",
    "gateway": "healthy"
  }
  ```

- `503 Service Unavailable` (gateway unreachable):

  ```json
  {
    "status": "degraded",
    "service": "model-platform-admin",
    "gateway": "unreachable",
    "error": "dial tcp 127.0.0.1:4000: connect: connection refused"
  }
  ```

---

### 1.2 Managed Model Catalog & Rollout Tasks

#### `GET /api/v1/admin/model-platform/models`

Lists every catalog model sorted by `model_name`, enriched with per-tenant state counts (`active`, `deprecated`, `tombstoned`) and live knowledgebase/assistant references (counting managed `platform` instances only).

**Response (`200 OK`):**

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

Onboards a model into the managed catalog and starts an asynchronous rollout to every `ready` tenant that does not yet have it. Re-publishing the same active `(model_name, model_type)` is idempotent and only enqueues tenants still missing it. **Never changes the catalog default**; however, if an administrator re-publishes a model that is already marked as the catalog default for tenants that lack it, the rollout worker will converge those tenants' users who have `platform_default` provenance or no preference record onto that default (preserving `user_explicit` users).

**Request Body:**

| Field | Type | Required | Constraints |
| :--- | :--- | :---: | :--- |
| `model_name` | `string` | Yes | Must match `^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$` and be present in gateway `GET /v1/models`. |
| `model_type` | `int` | Yes | One of `1, 2, 4, 8, 16, 32, 128`. |

```sh
curl -X POST http://127.0.0.1:9381/api/v1/admin/model-platform/models \
  -H "Authorization: ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"model_name":"deepseek-v3","model_type":1}'
```

**Response (`202 Accepted`):**

```json
{
  "code": 202,
  "data": {
    "task_id": "8c2f4f209c1a48a9b7b0e4c1d2e3f4a5",
    "total": 42
  }
}
```

**Error Codes:**

| HTTP Status | Condition |
| :---: | :--- |
| `400` | Invalid JSON, invalid `model_name` pattern, or unsupported `model_type`. |
| `409` | Catalog conflict (e.g., model exists with a different type or retired status). |
| `422` | Model is not currently routable through the LiteLLM gateway. |
| `502` | Gateway unreachable during route verification. |
| `503` | Gateway (`LITELLM_BASE_URL`) or database is not configured. |

---

#### `POST /api/v1/admin/model-platform/models/:id/retire`

Retires an active catalog model and launches an asynchronous rollout task processed per tenant by the background publish worker:

- **Embedding (`2`)**: Transitions tenants with referencing datasets to `deprecated` (read-only) and transitions tenants with zero references to `tombstoned` as each tenant item is processed.
- **Non-embedding (`1, 4, 8, 16, 32, 128`)**: Converges each tenant asynchronously to disabled/`tombstoned` and moves affected default users to the platform default.

Transient gateway/DB errors for a tenant item are retried with backoff; items reaching the retry limit (default 20) enter `failed` and can be inspected via `GET /api/v1/admin/model-platform/models/publish/tasks/:id` and re-queued via `POST /api/v1/admin/model-platform/models/publish/tasks/:id/retry`.

**Response (`202 Accepted`):** Same `Result` shape (`task_id`, `total`, optional `affected_users_count`).

**Error Codes:**

| HTTP Status | Condition |
| :---: | :--- |
| `400` | Attempted to retire a model that is currently the platform default for its type. |
| `404` | Catalog model `:id` not found. |

---

#### `POST /api/v1/admin/model-platform/models/:id/reactivate`

Reactivates a retired catalog model and launches a rollout restoring it to all ready tenants.

**Response (`202 Accepted`):** `{"code": 202, "data": {"task_id": "...", "total": 42}}`.

**Error Codes:** `404` (model not found), `409` (catalog state conflict).

---

#### `PUT /api/v1/admin/model-platform/models/:id/default`

Promotes an active catalog model to the platform default for its `model_type`. The rollout updates users whose preference provenance is `platform_default` **or who have no preference record** (even if their current default slot is non-empty), while preserving `user_explicit` selections.

**Response (`202 Accepted`):**

```json
{
  "code": 202,
  "data": {
    "task_id": "3a9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d",
    "total": 42
  }
}
```

**Error Codes:** `404` (model not found), `409` (model is retired / not active).

---

#### `GET /api/v1/admin/model-platform/models/publish/tasks/:id`

Returns derived rollout progress for task `:id` along with a paginated list of tenants that exhausted automatic retries.

**Query Parameters:**

| Parameter | Type | Default | Range |
| :--- | :--- | :---: | :--- |
| `page` | `int` | `1` | `>= 1` |
| `page_size` | `int` | `20` | `1..100` |

**Response (`200 OK`):**

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

`progress.status` values: `running` (`pending > 0`), `partial_failed` (`pending == 0 && failed > 0`), `completed` (`pending == 0 && failed == 0`).

**Error Codes:** `404` (task not found).

---

#### `POST /api/v1/admin/model-platform/models/publish/tasks/:id/retry`

Resets all `failed` items of task `:id` back to `pending` (`retry_count = 0`, `next_retry_at = NULL`) so the background publish worker immediately retries them.

**Response (`200 OK`):**

```json
{
  "code": 200,
  "data": {
    "retried": 1
  }
}
```

**Error Codes:** `404` (task not found).

---

### 1.3 Tenant Credentials & Emergency Key Rotation

#### `GET /api/v1/admin/model-platform/tenants`

Lists managed tenants and their gateway virtual key metadata using stable cursor pagination. **Never returns plaintext virtual keys.**

**Query Parameters:**

| Parameter | Type | Default | Description |
| :--- | :--- | :---: | :--- |
| `status` | `string` | `ready` | Reconcile status filter: `ready`, `pending`, `provisioning`, `degraded`, `failed`, `deleted`. |
| `after` | `string` | *(empty)* | Exclusive `tenant_id` cursor from the last item of the previous page. |
| `page_size` | `int` | `100` | Page size (`1..1000`). |

**Response (`200 OK`):**

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

- `key_fingerprint`: First 8 hex characters of the key's SHA-256 hash.
- `key_dirty_since`: Present only when superseded key revocation at the gateway has not yet converged.

---

#### `POST /api/v1/admin/model-platform/tenants/:tenant_id/rotate-key`

Issues a new LiteLLM virtual key for `:tenant_id`, updates the tenant's managed `platform` instance atomically (zero downtime), records the authenticated operator in the audit trail, and revokes superseded keys.

```sh
curl -X POST http://127.0.0.1:9381/api/v1/admin/model-platform/tenants/tenant_acme_01/rotate-key \
  -H "Authorization: ${ADMIN_TOKEN}"
```

**Success Responses:**

- `200 OK` — New key is active and **all superseded keys were revoked** (`old_keys_revoked: true`).
- `202 Accepted` — New key is active, but **revocation of one or more superseded keys is still converging** (`old_keys_revoked: false`). Waiting for the background `sweepDirtyKeys` loop retries only old-key revocation; calling this endpoint again executes another full rotation (issuing and switching to a different new key before attempting to revoke all superseded keys, without guaranteeing revocation succeeds if the gateway is still failing).

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

**Error Codes:**

| HTTP Status | Condition |
| :---: | :--- |
| `400` | Invalid `:tenant_id`. |
| `401` | Authenticated administrator identity (`user_id`/`email`) missing from context. |
| `404` | Tenant is not managed by the model platform. |
| `409` | Tenant is not in `ready` state, or another rotation for this tenant is already in flight. |
| `502` | Gateway unavailable before key cutover; the existing active key was preserved. |
| `503` | Gateway or database not configured. |

---

### 1.4 Usage Metering & Spend Isolation Audit

#### `GET /api/v1/admin/model-platform/usage/daily`

Returns paginated daily aggregated consumption records from `platform_tenant_usage_daily`.

**Query Parameters:**

| Parameter | Type | Default | Description |
| :--- | :--- | :---: | :--- |
| `tenant_id` | `string` | *(all)* | Exact tenant ID filter. |
| `model_name` | `string` | *(all)* | Exact model name filter. |
| `model_type` | `int` | *(all)* | One of `1, 2, 4, 8, 16, 32, 128` (invalid values return `400`). |
| `start_date` | `string` | *(open)* | Inclusive lower bound (`YYYY-MM-DD`). |
| `end_date` | `string` | *(open)* | Inclusive upper bound (`YYYY-MM-DD`). |
| `page` | `int` | `1` | `>= 1`. |
| `page_size` | `int` | `20` | `1..100`. |

**Response (`200 OK`):**

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

Returns high-level aggregated consumption totals across the same filter parameters (`tenant_id`, `model_name`, `model_type`, `start_date`, `end_date`), including per-model maps and a `by_model_type` breakdown.

**Response (`200 OK`):**

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

Conducts a live spend isolation audit against the records currently staged in the raw gateway table (`platform_gateway_spend_log`), comparing every staged `model_name` for `tenant_id` via exact string match against the tenant's current `tenant_model` rows under `OpenAI-API-Compatible` → `platform` (not filtered by model status). Passing (`audit_passed: true`) confirms that all currently staged gateway spend records match that current managed-model list; it does not cover traffic that bypassed the gateway, has not yet been collected, or was already purged by retention, nor does it prove that every historical call was authorized at the exact time it occurred (for example, if a now-disabled or retired row still exists in `tenant_model`).

**Query Parameters:**

| Parameter | Type | Required | Description |
| :--- | :--- | :---: | :--- |
| `tenant_id` | `string` | Yes | Target tenant ID (`400` if omitted or blank). |

```sh
curl "http://127.0.0.1:9381/api/v1/admin/model-platform/usage/audit?tenant_id=tenant_acme_01" \
  -H "Authorization: ${ADMIN_TOKEN}"
```

**Response (`200 OK` — Clean):**

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

**Response (`200 OK` — Violation Detected):**

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

## Part 2: App API Service Guard Contracts (`/api/v1`)

In `extensions/model-platform/backend/router/app_routes.go`, the extension registers **zero new standalone user routes**. Instead, it mounts three middlewares onto the upstream protected API router group (`RegisterModelPlatformProtectedRoutes`) that govern existing RAGFlow user endpoints.

### 2.1 `ManagedProviderGuardMiddleware`

Protects platform-managed providers, `platform` instances, and their child models from tenant tampering while allowing all read-only endpoints (`GET`, connectivity verification) and all mutations on tenant-owned BYOK instances.

- **Intercepted Routes & Methods**:
  - `PUT`, `PATCH`, `DELETE` on `/api/v1/providers/*`, `/setting/model/provider/*`, `/admin/model/provider/*` (excluding `/dataset` and `/artifacts` paths).
  - `POST` on `.../instances` (instance creation) and `.../models` (model creation under an instance).
- **Enforced Rules & Responses**:

| Trigger | HTTP Status | Response Body |
| :--- | :---: | :--- |
| Modifying/deleting a managed provider, `platform` instance (by name or ID), or model under `platform`; creating/renaming any instance to `platform` (case-insensitive); or malformed/trailing JSON on guarded mutation | `403` | `{"code": 403, "message": "Platform-managed providers and models cannot be modified or deleted by tenants."}` |
| Batch instance drop (`DELETE .../instances`) with duplicate `instances` JSON keys | `400` | `{"code": 400, "message": "Duplicate instances key in request body"}` |
| Batch instance drop targeting more than `100` instances | `400` | `{"code": 400, "message": "Batch instance targets exceed limit"}` |
| Mutation JSON payload exceeding `MAX_JSON_CONTENT_LENGTH` (default `1 MiB`) | `413` | `{"code": 413, "message": "Request entity too large"}` |

---

### 2.2 `DatasetImmutabilityGuardMiddleware`

Prevents vector space corruption and retired-embedding bindings on dataset creation and updates. Inspects JSON fields `embd_id`, `embedding_model`, `tenant_embd_id`, and `embedding_id`.

- **Intercepted Routes & Methods**:
  - `POST /api/v1/datasets` (dataset creation)
  - `PUT /api/v1/datasets/:dataset_id` and `PATCH /api/v1/datasets/:dataset_id` (dataset update)
- **Enforced Rules & Responses**:

| Trigger | HTTP Status | Exact Message Template |
| :--- | :---: | :--- |
| Creating a dataset with a retired/deprecated/read-only embedding model, or updating a dataset to switch to a different retired/deprecated/read-only embedding model | `400` | `Embedding model "<model>" is retired/deprecated and cannot be used for knowledgebases. Please choose an active model.` |
| Creating a dataset without an explicit embedding when the requesting user's default embedding is retired/deprecated | `400` | `Tenant default embedding model "<model>" is retired/deprecated. Please specify an active embedding model or update tenant default setting.` |
| Changing the embedding model of a dataset that already has `chunk_num > 0`, `doc_num > 0`, or active rows in `document` (to a different **active** model; if the new model is retired/deprecated, the retired-model check above takes precedence) | `400` | `Cannot modify embedding model of a dataset that already contains parsed documents or chunks. Create a new dataset to use a different embedding model.` |

> [!NOTE]
> Submitting the dataset's **existing** embedding model unchanged is always permitted. Changing the embedding model of a **completely empty** dataset to an **active** model is also permitted.

---

### 2.3 `DefaultModelPreferenceMiddleware`

Observes user-initiated changes to personal default model slots so `StickyGuard` preserves explicit choices during future platform default rollouts.

- **Observed Routes & Methods**:
  - `PATCH /api/v1/models`
  - `PATCH /api/v1/models/default`
  - `PATCH /api/v1/users/me/models`
- **Behavior**:
  - Does not alter upstream request/response formats or introduce new error codes.
  - Compares the requesting user's `user_tenant` default slots before and after the upstream handler executes.
  - When a slot changes to a different non-empty value and both default-slot reads (before and after the handler) plus the recording write succeed, records its provenance as `user_explicit` under a row lock so subsequent `PUT /api/v1/admin/model-platform/models/:id/default` rollouts preserve it. If any of these steps fails, no `user_explicit` record is written, the error is attached only to the request context without altering the response, and a later default rollout can overwrite the choice. Selecting the same model again does not create a record.
