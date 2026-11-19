# Model Platform Implementation Plan & Task Breakdown

English | [简体中文](implementation-plan.zh-CN.md)

> [!NOTE]
> **Document Scope & Division of Labor**:
> - **System Architecture & ADRs**: Please refer to [architecture.md](architecture.md) (Chinese version: [architecture.zh-CN.md](architecture.zh-CN.md)) for high-level system boundaries, domain invariants, and Architectural Decision Records.
> - **Document Purpose**: Focuses on concrete **engineering implementation details, code extension seams, database schema migrations, 25-task engineering specifications, and end-to-end verification guides**. Together, these documents form a comprehensive blueprint for the extension.

---

## Table of Contents

- [1. Principles & Boundary Scope](#1-principles--boundary-scope)
  - [1.1 Core Implementation Objectives](#11-core-implementation-objectives)
  - [1.2 Six Engineering Principles at a Glance](#12-six-engineering-principles-at-a-glance)
- [2. Dimension 1: Build & Workspace Overlay Integration](#2-dimension-1-build--workspace-overlay-integration)
  - [2.1 Go ee Global Build Tag Exclusion Analysis](#21-go-ee-global-build-tag-exclusion-analysis)
  - [2.2 Affirmative Baseline Stubs (ee-baseline) & Symbol Partitioning](#22-affirmative-baseline-stubs-ee-baseline--symbol-partitioning)
  - [2.3 Thin Seam Trampolines](#23-thin-seam-trampolines)
  - [2.4 Frontend Web Overlay Seams & Enterprise Flag Defense](#24-frontend-web-overlay-seams--enterprise-flag-defense)
  - [2.5 Build-Time CI Guards](#25-build-time-ci-guards)
  - [2.6 Workspace Overlay Pipeline (apply-overlay.sh)](#26-workspace-overlay-pipeline-apply-overlaysh)
- [3. Dimension 2: LiteLLM Gateway Selection & Credential Pipeline](#3-dimension-2-litellm-gateway-selection--credential-pipeline)
  - [3.1 Gateway Infrastructure & Loopback Isolation (127.0.0.1:4000)](#31-gateway-infrastructure--loopback-isolation-1270014000)
  - [3.2 Root Provider Secret Physical Isolation](#32-root-provider-secret-physical-isolation)
  - [3.3 Scoped Tenant Virtual Key Specification (sk-tenant-...)](#33-scoped-tenant-virtual-key-specification-sk-tenant-)
  - [3.4 Gateway Permission Routing & Admin Endpoint Blocking](#34-gateway-permission-routing--admin-endpoint-blocking)
  - [3.5 Tenant Decommissioning & Virtual Key Revocation](#35-tenant-decommissioning--virtual-key-revocation)
- [4. Dimension 3: Tenant Lifecycle & Self-Healing Reconciler](#4-dimension-3-tenant-lifecycle--self-healing-reconciler)
  - [4.1 GORM Post-Commit Asynchronous Decoupling](#41-gorm-post-commit-asynchronous-decoupling)
  - [4.2 Two-Tier Self-Healing Controller Architecture](#42-two-tier-self-healing-controller-architecture)
  - [4.3 State Machine Model & Migration Schema (platform_tenant_reconcile)](#43-state-machine-model--migration-schema-platform_tenant_reconcile)
  - [4.4 Native Driver Resolution & Compensating Rollback](#44-native-driver-resolution--compensating-rollback)
  - [4.5 Backend Managed Model Protection (ManagedProviderGuardMiddleware)](#45-backend-managed-model-protection-managedproviderguardmiddleware)
- [5. Dimension 4: Model Defaults & Dataset Vector Space Protection](#5-dimension-4-model-defaults--dataset-vector-space-protection)
  - [5.1 Atomic Conditional SQL for Default Initialization](#51-atomic-conditional-sql-for-default-initialization)
  - [5.2 Explicit Preference Tracking & Sticky Guard](#52-explicit-preference-tracking--sticky-guard)
  - [5.3 Three-Stage Embedding Lifecycle Protection](#53-three-stage-embedding-lifecycle-protection)
  - [5.4 Dataset Vector Space Immutability & Deprecated Model Guard (DatasetEmbeddingGuardMiddleware)](#54-dataset-vector-space-immutability--deprecated-model-guard-datasetembeddingguardmiddleware)
- [6. Dimension 5: Metering & Authoritative Reconciliation Pipeline](#6-dimension-5-metering--authoritative-reconciliation-pipeline)
  - [6.1 Authoritative Gateway Spend Logs & Raw Staging Deduplication](#61-authoritative-gateway-spend-logs--raw-staging-deduplication)
  - [6.2 48-Hour Rolling Lookback Replacement Upsert Pipeline](#62-48-hour-rolling-lookback-replacement-upsert-pipeline)
  - [6.3 Physical Zero-Billing Isolation for Self-Owned Models](#63-physical-zero-billing-isolation-for-self-owned-models)
  - [6.4 Admin Usage Statistics & Streaming CSV Export API](#64-admin-usage-statistics--streaming-csv-export-api)
- [7. 25-Task Engineering Breakdown (Phase 0 ~ Phase 5)](#7-25-task-engineering-breakdown-phase-0--phase-5)
  - [7.1 Executive Task Matrix](#71-executive-task-matrix)
  - [7.2 Architecture Breakdown & Phase Evolution Diagram](#72-architecture-breakdown--phase-evolution-diagram)
  - [7.3 Detailed Engineering Task Specifications](#73-detailed-engineering-task-specifications)
- [8. End-to-End Integration & Verification Guide](#8-end-to-end-integration--verification-guide)
  - [8.1 Target & Scope Definition (Core Capability MVP)](#81-target--scope-definition-core-capability-mvp)
  - [8.2 End-to-End Sequence Diagram](#82-end-to-end-sequence-diagram)
  - [8.3 Failure Modes & Fault Tolerance Matrix](#83-failure-modes--fault-tolerance-matrix)
  - [8.4 Step-by-Step Execution & Verification Commands](#84-step-by-step-execution--verification-commands)

---

## 1. Principles & Boundary Scope

### 1.1 Core Implementation Objectives
The `model-platform` extension introduces multi-tenant model hosting capabilities into `oh-my-ragflow`. Platform administrators can provision pre-configured Large Language Models (LLM) and Embedding models for tenants, while tenants retain full freedom to configure self-owned providers.

### 1.2 Six Engineering Principles at a Glance
For architectural rationale and domain invariant proofs, please refer directly to [architecture.md](architecture.md#3-core-invariant-guarantees). At the engineering implementation level, all tasks strictly adhere to six mandatory constraints:
1. **Strict Non-Invasive Architecture**: The `upstream/ragflow` submodule directory must remain 100% clean and unmodified (`git status --porcelain` is strictly empty).
2. **Native Driver Resolution**: Managed providers use `ProviderName = "OpenAI-API-Compatible"` with `InstanceName = "platform"`, reusing upstream drivers without modifying upstream factory code or triggering dummy stubs.
3. **Physical Credential Isolation**: Root upstream API keys reside exclusively in the LiteLLM gateway environment. The proxy binds strictly to loopback `127.0.0.1:4000`. Tenants receive only scoped virtual keys (`sk-tenant-...`).
4. **GORM Post-Commit Decoupling**: No remote network calls may execute inside upstream database transactions. Reconcilers trigger via GORM `AfterCommit` events and feature a two-tier self-healing controller surviving 45-second gateway outages.
5. **Atomic Defaults & Immutable Datasets**: Concurrency races are eliminated via atomic conditional SQL. Explicit user choices are tracked in preference tables. Parsed datasets with chunks have strictly immutable vector spaces (HTTP 400).
6. **Authoritative Spend Logs & Mathematical Idempotence**: LiteLLM spend logs serve as the sole truth boundary, staged with `INSERT IGNORE` on `spend_id`. Daily usage aggregates over a 48-hour day-aligned lookback window using replacement upserts (`call_count = VALUES(call_count)`).

---

## 2. Dimension 1: Build & Workspace Overlay Integration

### 2.1 Go ee Global Build Tag Exclusion Analysis
In upstream RAGFlow, enterprise features are guarded by Go build tags:
- The build script `upstream/ragflow/build.sh:32-34` injects `RAGFLOW_GO_TAGS`:
  ```bash
  EXTRA_GO_TAGS="${RAGFLOW_GO_TAGS:-}"
  GO_BUILD_TAGS="cgo,static,sonic"
  [ -n "$EXTRA_GO_TAGS" ] && GO_BUILD_TAGS="$GO_BUILD_TAGS,$EXTRA_GO_TAGS"
  ```
- Any upstream file constrained by `//go:build !ee` is globally excluded by the Go compiler when `-tags ee` is supplied.
- **The Global Exclusion Trap**: Go build tags are global compiler directives. Once `RAGFLOW_GO_TAGS=ee` is supplied, all upstream `!ee` community stub files are excluded simultaneously. If an extension only stubs the packages it immediately needs, the compiler fails on all remaining packages:
  1. Packages containing only `stub.go` (such as `clickhouse` and `billing`) trigger `build constraints exclude all Go files`.
  2. Callers of unstubbed packages trigger `undefined symbol` compiler errors.

### 2.2 Affirmative Baseline Stubs (ee-baseline) & Symbol Partitioning
To eliminate this trap without touching upstream, `oh-my-ragflow` establishes an affirmative **`ee-baseline` overlay**. This baseline provides community-equivalent `//go:build ee` fallback stubs across all 19 affected upstream packages.

#### Symbol Partitioning Rule
Go strictly prohibits duplicate symbol declarations within the same package. The `model-platform` extension injects custom logic into `internal/admin` and `internal/router`. To avoid `redeclared in this package` errors, symbols follow strict partitioning and precedence:
1. **Non-Router Stubs in Baseline**: `ee-baseline` stubs pure packages and non-router fallback symbols in `internal/admin` and `internal/router` (such as `AppendServiceList`, `GetEEServicesStatus`, `ToggleChat`).
2. **Router Registration Exclusively in Trampolines**: Route registration symbols (`RegisterEERouter`, `SetupEERouter`, `RegisterEEProtectedRouter`, `RegisterEENoAuthRouter`, `RegisterEEAuthRouter`) must **never be declared in `ee-baseline`**. They are uniquely declared in `overlay/trampolines/internal/admin/` and `overlay/trampolines/internal/router/`.
3. **Overlay Application Precedence**: `apply-overlay.sh` synchronizes layers in order: `ee-baseline -> trampolines -> backend -> web`.

The 19 Go source packages and 9 test packages partitioned across layers are:

| # | Package Path | Community Source File | Exported Symbols / Structs | Partitioning Strategy |
|---|---|---|---|---|
| 1 | `cmd` | `root_stub.go` | `runWithSysParamsEE` | `ee-baseline` fallback |
| 2 | `internal/admin` | `router_stub.go` | `AppendServiceList`, `GetEEServicesStatus`, `ToggleChat` | Non-router stubs in `ee-baseline`; `RegisterEERouter` reserved for trampoline |
| 3 | `internal/cli` | `main_stub.go` | `executeEECommand`, `runAgentEE` | `ee-baseline` fallback |
| 4 | `internal/common` | `auth_stub.go` | `AuthHookEE`, `ValidateTokenEE` | `ee-baseline` fallback |
| 5 | `internal/dao` | `tenant_stub.go` | `applyTenantPolicyEE`, `initializeTenantDefaultRoleEE` | `ee-baseline` fallback |
| 6 | `internal/deepdoc/parser/pdf/table` | `table_construct_vh_stub.go` | `TableConstructVHEE` | `ee-baseline` fallback |
| 7 | `internal/engine/clickhouse` | `stub.go` | `ClickhouseEngineEE` | Isolated stub in `ee-baseline` |
| 8 | `internal/entity` | `entity_stub.go` | `TenantMetadataEE` | `ee-baseline` fallback |
| 9 | `internal/handler` | `handler_stub.go` | `RegisterEEHandlers` | `ee-baseline` fallback |
| 10 | `internal/permission` | `permission_stub.go` | `EnforceTenantPermissionEE` | `ee-baseline` fallback |
| 11 | `internal/permission/testsupport` | `test_stub.go` | `NewMockPermissionManagerEE` | `ee-baseline` fallback |
| 12 | `internal/router` | `router_stub.go` | `registerCustomRoutesEE` | Router seam reserved for trampoline |
| 13 | `internal/server/config` | `config_stub.go` | `EEConfig`, `LoadEEConfig` | `ee-baseline` fallback |
| 14 | `internal/server/local` | `local_stub.go` | `InitLocalEE` | `ee-baseline` fallback |
| 15 | `internal/server` | `server_stub.go` | `ServerHooksEE` | `ee-baseline` fallback |
| 16 | `internal/service/billing` | `stub.go` | `BillingServiceEE` | Isolated stub in `ee-baseline` |
| 17 | `internal/service/dataset` | `dataset_stub.go` | `DatasetPermissionEE` | `ee-baseline` fallback |
| 18 | `internal/service/document` | `document_stub.go` | `ParseDocumentEE` | `ee-baseline` fallback |
| 19 | `internal/service` | `service_stub.go` | `UserServiceEE`, `TenantServiceEE` | `ee-baseline` fallback |

### 2.3 Thin Seam Trampolines
Trampolines act as thin bridges redirecting upstream extension points to the `model-platform` package:
1. **Admin Router Trampoline** (`overlay/trampolines/internal/admin/model_platform_trampoline_ee.go`):
   ```go
   //go:build ee

   package admin

   import (
       "github.com/gin-gonic/gin"
       mpAdmin "ragflow/extensions/model-platform/backend/admin"
   )

   func RegisterEERouter(protected *gin.RouterGroup, r *Router) {
       mpAdmin.RegisterModelPlatformAdminRoutes(protected)
   }
   ```
2. **Main Router Trampoline** (`overlay/trampolines/internal/router/model_platform_trampoline_ee.go`):
   ```go
   //go:build ee

   package router

   import (
       "github.com/gin-gonic/gin"
       "ragflow/extensions/model-platform/backend/guard"
   )

   func SetupEERouter(r *gin.Engine) {
       r.Use(guard.ManagedProviderGuardMiddleware())
       r.Use(guard.DatasetImmutabilityGuardMiddleware())
   }
   ```

### 2.4 Frontend Web Overlay Seams & Enterprise Flag Defense
In `upstream/ragflow/web/src/pages/admin/utils.tsx:80`, `IS_ENTERPRISE = import.meta.env.VITE_RAGFLOW_ENTERPRISE === 'RAGFLOW_ENTERPRISE'`.
> [!WARNING]
> Enabling `VITE_RAGFLOW_ENTERPRISE` blindly mounts un-stubbed upstream enterprise routes (such as Whitelist, Roles, Monitoring), triggering broken routes and unauthenticated 404/401 API crashes.

The `model-platform` extension leaves `VITE_RAGFLOW_ENTERPRISE=false` disabled and injects its UI strictly through targeted frontend seams:
- `overlay/web/src/ee/merge-routes.tsx`: Mounts `/admin/model-providers` and `/admin/model-usage-statistics`.
- `overlay/web/src/ee/pages/admin/layouts/navigation-private.tsx`: Injects admin sidebar navigation items.
- `overlay/web/src/ee/utils/api-private.ts`: Registers model platform backend API routes.

### 2.5 Build-Time CI Guards
The script `extensions/model-platform/scripts/audit-non-invasive.sh` enforces three automated CI gates:
1. **Gate 1 (Clean Submodule)**: Asserts `git -C upstream/ragflow diff --quiet` and `status --porcelain` are 100% clean.
2. **Gate 2 (Enterprise Flag Guard)**: Fails immediately if `VITE_RAGFLOW_ENTERPRISE` equals `RAGFLOW_ENTERPRISE`.
3. **Gate 3 (19-Package EE Baseline Completeness Scan)**: Scans all upstream packages guarded by `//go:build !ee` to assert that all 19 packages have affirmative `//go:build ee` fallback stubs present in `overlay/ee-baseline/` and `overlay/trampolines/`.

### 2.6 Workspace Overlay Pipeline (apply-overlay.sh)
The script `extensions/model-platform/scripts/apply-overlay.sh` automates syncing:
```bash
bash extensions/model-platform/scripts/apply-overlay.sh
```
It validates all three pre-flight assertions, prepares a clean upstream workspace (`rm -rf build/ragflow && mkdir -p build/ragflow`), extracts upstream baseline via `git archive`, and synchronizes layers in order (`ee-baseline`, `trampolines`, `backend`, and `overlay/web/src/ee/` into `build/ragflow/web/src/ee/`).

---

## 3. Dimension 2: LiteLLM Gateway Selection & Credential Pipeline

### 3.1 Gateway Infrastructure & Loopback Isolation (127.0.0.1:4000)
LiteLLM Proxy (`ghcr.io/berriai/litellm:main-v1.110.0` - latest stable v1.110.0) backed by PostgreSQL (`postgres:16-alpine`) provides the gateway layer:
- **Physical Loopback Isolation**: The container binds strictly to loopback `127.0.0.1:4000` (Docker ports mapping: `127.0.0.1:4000:4000`). Public internet exposure is strictly prohibited.
- **Protocol Compatibility**: Native OpenAI-compatible `/v1/chat/completions`, `/v1/embeddings`, and `/v1/models` match RAGFlow's `OpenAI-API-Compatible` driver directly.

### 3.2 Root Provider Secret Physical Isolation
```mermaid
flowchart TD
    subgraph SecretBoundary [Physical Boundary: Gateway Container Environment]
        EnvSecrets[Upstream Secrets: PLATFORM_UPSTREAM_OPENAI_KEY]
        LiteLLMConfig[litellm-config.yaml]
    end

    subgraph LoopbackNetwork [Loopback Network: 127.0.0.1:4000]
        LiteLLMProxy[LiteLLM Proxy Engine]
        PostgresDB[(PostgreSQL: LiteLLM_VerificationToken)]
    end

    subgraph ControlPlane [RAGFlow Engine & Reconciler]
        Reconciler[Model Platform Reconciler]
        TenantDB[(RAGFlow DB: user_tenant, tenant_model)]
        ModelFactory[RAGFlow ModelFactory]
    end

    EnvSecrets --> LiteLLMProxy
    LiteLLMConfig --> LiteLLMProxy
    PostgresDB <--> LiteLLMProxy
    Reconciler -->|Master Key via 127.0.0.1: /key/generate| LiteLLMProxy
    LiteLLMProxy -->|Returns Virtual Key: sk-tenant-*| Reconciler
    Reconciler -->|Stores Virtual Key| TenantDB
    ModelFactory -->|Inference via 127.0.0.1:4000| LiteLLMProxy
    LiteLLMProxy -->|Proxies with Root Secret| UpstreamProvider[Upstream Provider API]
```

### 3.3 Scoped Tenant Virtual Key Specification (sk-tenant-...)
Virtual keys are provisioned via `POST /key/generate`:
```json
{
  "models": ["gpt-4o-mini", "text-embedding-3-small"],
  "team_id": "tenant-e2e-test",
  "user_id": "user-e2e-test",
  "key_alias": "tenant_tenant-e2e-test",
  "metadata": {"tenant_id": "tenant-e2e-test"}
}
```
The client in `backend/gateway/client.go` dynamically loads `LITELLM_MASTER_KEY` and `LITELLM_BASE_URL` with 5-second HTTP timeouts and exponential retries.

### 3.4 Gateway Permission Routing & Admin Endpoint Blocking
LiteLLM enforces:
- Admin endpoints (`/key/*`, `/model/*`, `/global/*`) require the Master Key. Requests using tenant virtual keys return HTTP 401 or 403.
- Invocations to models outside the tenant's model whitelist return HTTP 403 Forbidden (`ModelNotAllowedError`).

### 3.5 Tenant Decommissioning & Virtual Key Revocation
When a tenant is deleted, `TenantDecommissioner` asynchronously invokes `POST /key/delete` (with a JSON payload containing `keys` or `key_aliases`) to revoke the virtual key physically from PostgreSQL.

---

## 4. Dimension 3: Tenant Lifecycle & Self-Healing Reconciler

### 4.1 GORM Post-Commit Asynchronous Decoupling
To avoid long-lived database locks and transaction rollbacks during network calls, hooks decouple via GORM:
1. `RegisterPostCommitHook` attaches to transaction `AfterCommit` events.
2. Events dispatch to an in-memory buffered channel on `event.EventBus`.
3. `TenantHookPrecheck` verifies that tenant database rows are committed and externally visible before invoking the reconciler.

### 4.2 Two-Tier Self-Healing Controller Architecture
```mermaid
flowchart TD
    CommitEvent[Tenant Created Event] --> Tier1Queue[Tier 1: Fast In-Memory Queue]
    
    subgraph Tier1 [Tier 1: Transient Jitter Recovery]
        Tier1Queue --> Worker[Worker Process]
        Worker --> TryReconcile{Reconcile Gateway & DB}
        TryReconcile -->|Success| SetReady[State: ready]
        TryReconcile -->|Failure < 5 Retries| Backoff1[Exponential Backoff: 1s, 2s, 4s, 8s, 16s] --> Worker
        TryReconcile -->|Exhausted 5 Retries| SetDegraded[State: degraded]
    end

    subgraph Tier2 [Tier 2: Level-Triggered Poller]
        Poller[ReconcilePoller: Scans Every 30s] --> FindDegraded[Find pending & degraded Records]
        FindDegraded --> TryPoller{Gateway Available?}
        TryPoller -->|Yes| Heal[Heal Tenant & Set ready]
        TryPoller -->|No| Backoff2[Jittered Backoff: Up to 10m]
    end

    SetDegraded --> Poller
```

- **Tier 1 Fast In-Memory Queue**: New tenant events are processed immediately by the in-memory queue with 5 retries (1s, 2s, 4s, 8s, 16s with jitter, ~31s total).
- **Tier 2 Level-Triggered Poller**: Scans every 30s for records in `pending` or `degraded` state whose `next_retry_at <= NOW()`. Handles extended outages (e.g., 45-second gateway restarts) using longer backoffs (1m, 2m, 5m, capped at 10m, up to 50 retries / 7 days).
- **Provisioning Lease Timeout**: Reclaims abandoned `provisioning` records where `updated_at < NOW() - INTERVAL 5 MINUTE` (e.g., if a worker node crashes mid-flight), resetting them back to `pending`/`degraded` for safe recovery.
- **Compensating Rollback**: If database persistence fails after generating a virtual key on LiteLLM, the worker immediately issues `POST /key/delete` (with `keys` or `key_aliases`) to prevent orphaned credentials.
- **Distributed Row Locking**: Concurrent pollers use `SELECT * FROM platform_tenant_reconcile WHERE status IN ('pending', 'degraded') AND next_retry_at <= NOW() LIMIT 50 FOR UPDATE SKIP LOCKED` (or optimistic version locking `SET status = 'provisioning', version = version + 1 WHERE tenant_id = ? AND version = ?`).

### 4.3 State Machine Model & Migration Schema (platform_tenant_reconcile)
The table `platform_tenant_reconcile` manages lifecycle state:
```sql
CREATE TABLE IF NOT EXISTS platform_tenant_reconcile (
    tenant_id VARCHAR(32) PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
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

### 4.4 Native Driver Resolution & Compensating Rollback
- Records are stored as `ProviderName = "OpenAI-API-Compatible"` with `InstanceName = "platform"` and `is_managed = true`.
- Upstream `ModelFactory.CreateModelDriver` matches `"openai-api-compatible"` directly.
- **Compensating Rollback**: If database persistence fails after virtual key generation, `reconciler` immediately invokes `POST /key/delete` (with `keys` or `key_aliases`) to clean up orphaned credentials.

### 4.5 Backend Managed Model Protection (ManagedProviderGuardMiddleware)
`ManagedProviderGuardMiddleware` intercepts write requests (`DELETE`, `PUT`, `PATCH`) to `/v1/user/setting/model/provider`, `/api/v1/user/setting/model/provider`, and `/api/v1/admin/model/provider`:
- If target provider is `OpenAI-API-Compatible@platform` or `is_managed = true`, it returns HTTP 403 Forbidden with `"Platform-managed providers and models cannot be modified or deleted by tenants."`

---

## 5. Dimension 4: Model Defaults & Dataset Vector Space Protection

### 5.1 Atomic Conditional SQL for Default Initialization
To eliminate concurrency races when assigning initial default models, LLM and Embedding models are updated with independent atomic conditional queries:
```sql
-- Initialize default chat model (only if unset)
UPDATE user_tenant
SET llm_id = 'gpt-4o-mini@platform@OpenAI-API-Compatible',
    tenant_llm_id = ?,
    updated_at = NOW()
WHERE user_id = ?
  AND tenant_id = ?
  AND (llm_id IS NULL OR llm_id = '');

-- Initialize default embedding model (only if unset)
UPDATE user_tenant
SET embd_id = 'text-embedding-3-small@platform@OpenAI-API-Compatible',
    tenant_embd_id = ?,
    updated_at = NOW()
WHERE user_id = ?
  AND tenant_id = ?
  AND (embd_id IS NULL OR embd_id = '');
```

### 5.2 Explicit Preference Tracking & Sticky Guard
The table `platform_user_default_preference` records user choice provenance:
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
Platform upgrades to default models check `origin == 'platform_default'` and preserve explicit user selections (`origin == 'user_explicit'`).

### 5.3 Three-Stage Embedding Lifecycle Protection
```mermaid
stateDiagram-v2
    [*] --> Active: Admin publishes model
    Active --> DeprecatedReadOnly: Admin disables model (Active KB references > 0)
    Active --> Tombstoned: Admin disables model (Active KB references == 0)
    
    state DeprecatedReadOnly {
        [*] --> PreservedInGateway: Retain LiteLLM virtual key whitelist
        PreservedInGateway --> ActiveInDB: tenant_model.status stays "active"
        ActiveInDB --> HiddenForNewKBs: Exclude from frontend & creation APIs
    }

    DeprecatedReadOnly --> Tombstoned: All KBs migrated or deleted (references == 0)

    state Tombstoned {
        [*] --> EvictFromGateway: LiteLLM POST /key/update evicts from whitelist
        EvictFromGateway --> DisableInDB: tenant_model.status = "disabled"
    }
    
    Tombstoned --> [*]
```

1. `Active`: Available for new knowledge base creation and existing dataset vector search.
2. `Deprecated/Read-Only`: Hidden from new dataset creation, but retained in gateway whitelists so existing knowledge bases continue vector search.
3. `Tombstoned`: Physically revoked only when active dataset reference count drops to 0.

### 5.4 Dataset Vector Space Immutability & Deprecated Model Guard (DatasetEmbeddingGuardMiddleware)
`DatasetEmbeddingGuardMiddleware` (aliased as `DatasetImmutabilityGuardMiddleware`) intercepts both dataset creation (`POST /api/v1/datasets`) and mutation endpoints (`PUT/PATCH /api/v1/datasets/:id`):
- **Creation Guard**: Blocks binding to retired/deprecated embedding models (either explicitly requested or resolved from tenant default). Returns HTTP 400 Bad Request.
- **Empty Dataset Mutation Guard**: Blocks switching an empty dataset's embedding model to a retired/deprecated model. Returns HTTP 400 Bad Request.
- **Non-Empty Immutability Guard**: If the dataset contains parsed documents or chunks (`chunk_num > 0 || doc_num > 0`) and the request mutates `embedding_model`, it aborts with HTTP 400 Bad Request.

---

## 6. Dimension 5: Metering & Authoritative Reconciliation Pipeline

### 6.1 Authoritative Gateway Spend Logs & Raw Staging Deduplication
LiteLLM spend logs are staged into `platform_gateway_spend_log`:
```sql
CREATE TABLE IF NOT EXISTS platform_gateway_spend_log (
    spend_id VARCHAR(128) PRIMARY KEY,
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
```
Network retries insert with `INSERT IGNORE`, guaranteeing atomic deduplication on `spend_id`.

### 6.2 48-Hour Rolling Lookback Replacement Upsert Pipeline
Daily usage rolls up into `platform_tenant_usage_daily`:
```sql
CREATE TABLE IF NOT EXISTS platform_tenant_usage_daily (
    tenant_id VARCHAR(64) NOT NULL,
    model_name VARCHAR(128) NOT NULL,
    usage_date VARCHAR(10) NOT NULL, -- Format: YYYY-MM-DD
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

INSERT INTO platform_tenant_usage_daily (
    tenant_id, model_name, usage_date, call_count, prompt_tokens, completion_tokens, total_tokens,
    cached_tokens, cache_write_tokens, reasoning_tokens, total_spend, prompt_spend, completion_spend, cache_savings, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW())
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
Overwriting instead of accumulating ensures mathematical idempotence across multiple reconciliation runs.

```mermaid
flowchart TD
    LiteLLM[("LiteLLM Proxy Spend Logs")] -->|"Periodic Window Pull (48h Lookback)"| Collector["Spend Log Collector"]
    
    subgraph Tier1_Staging ["Tier 1: Raw Staging & Deduplication"]
        Collector -->|"INSERT IGNORE INTO spend_id"| SpendLogTable[("platform_gateway_spend_log<br>Primary Key: spend_id")]
    end

    subgraph Tier2_Aggregation ["Tier 2: Deterministic Replacement Rollup"]
        SpendLogTable -->|"GROUP BY tenant, model, usage_date<br>SUM(tokens), SUM(spend)"| RollupEngine["Rollup Engine"]
        RollupEngine -->|"ON DUPLICATE KEY UPDATE<br>call_count = VALUES(call_count)"| DailyTable[("platform_tenant_usage_daily<br>Primary Key: tenant_id, model, date")]
    end
```

### 6.3 Physical Zero-Billing Isolation for Self-Owned Models
Tenant self-owned models connect directly to third-party endpoints. They completely bypass the LiteLLM gateway, generating 0 gateway spend logs and strictly $0.00 platform spend.

```mermaid
flowchart LR
    subgraph TenantWorkloads ["Tenant Inbound Requests"]
        ReqPlatform["Platform Model: gpt-4o-mini"]
        ReqCustom["Tenant Model: Custom OpenAI / Ollama"]
    end

    subgraph RAGFlowRuntime ["RAGFlow Runtime ModelFactory"]
        ReqPlatform --> Dispatcher{"ModelFactory Dispatcher"}
        ReqCustom --> Dispatcher
    end

    subgraph DirectCall ["Direct Route: Physical Gateway Bypass"]
        Dispatcher -->|"Tenant Private Credentials & BaseURL"| ExternalTenantAPI["External Tenant API Endpoint"]
    end

    subgraph GatewayCall ["Platform Route: Full Metering Pipeline"]
        Dispatcher -->|"sk-tenant-xxx via LiteLLM 127.0.0.1:4000"| LiteLLMProxy["LiteLLM Proxy Engine"]
        LiteLLMProxy --> AuthoritativeLogs[("LiteLLM Spend Logs")]
        LiteLLMProxy --> ProviderAPI["Upstream Provider API (OpenAI / DeepSeek)"]
    end
```

### 6.4 Admin Usage Statistics & Streaming CSV Export API
- **Paginated Daily Usage API**: `GET /admin/model-platform/usage/daily` (proxied as `/api/v1/admin/model-platform/usage/daily`)
  - Supports filtering by `tenant_id`, `model_name`, `start_date`, and `end_date`.
- **Usage Summary API**: `GET /admin/model-platform/usage/summary`
  - Returns tenant-level and platform-level token and spend aggregation.
- **Streaming CSV Export API**: `GET /admin/model-platform/usage/export`
  - Streams CSV exports for millions of billing records with minimal memory footprint.
- **Self-Owned Zero Billing Audit API**: `GET /admin/model-platform/usage/audit`
  - Verifies zero gateway spend logs generated by tenant custom providers.

---

## 7. 25-Task Engineering Breakdown (Phase 0 ~ Phase 5)

### 7.1 Executive Task Matrix

| Phase | Task ID | Core Objective | Key Deliverables & Target Files | Verification Method |
| :--- | :--- | :--- | :--- | :--- |
| **P0: Build Baseline** | **TASK-0.1** | Stub 19 EE Packages | Affirmative `//go:build ee` stubs (`overlay/ee-baseline/**/stub_ee.go`) | `RAGFLOW_GO_TAGS=ee go build` compiles cleanly |
| | **TASK-0.2** | Overlay Sync Pipeline | Workspace overlay script & pre-flight assertions (`scripts/apply-overlay.sh`) | Script execution & zero upstream diff check |
| | **TASK-0.3** | Thin Trampolines & 403 Guard | Router trampolines & managed provider guard (`managed_provider_guard.go`, `trampolines/**`) | Unit tests assert 403 on DELETE/PUT |
| | **TASK-0.4** | Frontend Web Overlay Seams | Admin route & navigation injection (`merge-routes.tsx`, `navigation-private.tsx`) | `VITE_RAGFLOW_ENTERPRISE=false` build check |
| **P1: Gateway Infra** | **TASK-1.1** | LiteLLM Baseline & Isolation | Loopback network isolation & Docker Compose (`gateway/docker-compose.yml`, `litellm-config.yaml`) | `validate-gateway-config.sh` & port checks |
| | **TASK-1.2** | Root Secret Isolation | Upstream key injection & log masking (`litellm-config.yaml`, `test_secret_isolation.py`) | Secret isolation tests & console log audit |
| | **TASK-1.3** | Virtual Key Client & Timeouts | Go HTTP client & timeout controls (`backend/gateway/client.go`, `types.go`) | Client unit tests with timeout verification |
| | **TASK-1.4** | Gateway Permission Routing | Model whitelist & management endpoint blocking (`permission_test.go`, `test_gateway_permissions.py`) | 401/403 tests on unauthorized endpoints |
| **P2: Tenant Lifecycle** | **TASK-2.1** | Post-Commit Event Decoupling | GORM transaction post-commit dispatch (`backend/dao/tenant_hook.go`, `event/bus.go`) | Post-commit dispatch & precheck unit tests |
| | **TASK-2.2** | Reconciler State Machine | Reconcile schema migration & optimistic lock (`entity/reconcile.go`, `dao/reconcile.go`) | State machine transition & lock conflict tests |
| | **TASK-2.3** | Native Driver Resolution | `OpenAI-API-Compatible@platform` sync & rollback (`tenant_reconciler.go`) | Native driver match & rollback revocation test |
| | **TASK-2.4** | Two-Tier Self-Healing Poller | Fast retry worker + level-triggered poller (`reconciler/poller.go`, `retry.go`) | Outage recovery test under 45s downtime |
| | **TASK-2.5** | Tenant Decommissioning | Tenant deletion event capture & key cleanup (`tenant_cleanup.go`) | Decommissioning test & physical revocation |
| **P3: Defaults Governance** | **TASK-3.1** | Atomic Conditional Defaults | Atomic conditional SQL default initializer (`defaults_manager.go`) | Concurrency race tests & atomic update checks |
| | **TASK-3.2** | Explicit Intent Provenance | User preference tracking table (`entity/preference.go`, `sticky_guard.go`) | Upgrade test asserting preserved explicit choices |
| | **TASK-3.3** | 3-Stage Embedding Lifecycle | `active` -> `deprecated` -> `tombstoned` transitions (`embedding_lifecycle.go`) | Read-only vector search & hidden creation tests |
| | **TASK-3.4** | Dataset Immutability Guard | Guard blocking embedding model mutation on parsed datasets (`dataset_immutability_guard.go`) | Middleware test asserting HTTP 400 on mutation |
| **P4: Metering & Rollup** | **TASK-4.1** | Spend Log Staging & Deduplication | Raw staging table & `INSERT IGNORE` (`entity/spend_log.go`, `collector.go`) | Deduplication tests with duplicate log inputs |
| | **TASK-4.2** | 48-Hour Daily Usage Pipeline | Day-aligned rolling lookback replacement upsert (`entity/usage.go`, `pipeline.go`) | Multiple rollup runs asserting identical totals |
| | **TASK-4.3** | Self-Owned Model Zero Billing | Verification of direct external provider bypass (`isolation_audit.go`) | Audit test asserting 0 gateway logs and $0 spend |
| | **TASK-4.4** | Admin Usage & CSV Export API | Usage statistics query & streaming CSV export (`admin/usage_handler.go`) | HTTP handler unit tests & streaming CSV check |
| **P5: E2E Integration QA** | **TASK-5.1** | Isolated Test Cluster Orchestration | PostgreSQL + LiteLLM test cluster (`tests/e2e/docker-compose.test.yml`, `setup_env.sh`) | Health check probes & network boundary tests |
| | **TASK-5.2** | Native Driver E2E Integration | Go native driver Chat + Embedding live integration (`tests/e2e/e2e_core_test.go`) | Full E2E core test suite passes |
| | **TASK-5.3** | Security, Resilience & Guards | 403 anti-deletion, 400 immutability, 45s outage tests (`tests/e2e/e2e_security_stability_test.go`) | Full security and stability E2E suite passes |
| | **TASK-5.4** | Non-Invasive Audit & CI Gates | Audit script & GitHub Actions workflow (`audit-non-invasive.sh`, `ci.yml`) | CI gates pass with Exit Code 0 |

### 7.2 Architecture Breakdown & Phase Evolution Diagram

```mermaid
flowchart TD
    subgraph P0 ["Phase 0: Build & Overlay Foundation"]
        T0_1["TASK-0.1 Stub 19 EE Packages"] --> T0_2["TASK-0.2 Workspace Overlay Script"]
        T0_2 --> T0_3["TASK-0.3 Thin Trampolines & 403 Guard"]
        T0_3 --> T0_4["TASK-0.4 Frontend Web Overlay Seams"]
    end

    subgraph P1 ["Phase 1: Gateway & Credential Isolation"]
        T1_1["TASK-1.1 LiteLLM Baseline & Isolation"] --> T1_2["TASK-1.2 Root Secret Isolation"]
        T1_2 --> T1_3["TASK-1.3 Virtual Key Client & Timeouts"]
        T1_3 --> T1_4["TASK-1.4 Permission Routing & Endpoint Blocking"]
    end

    subgraph P2 ["Phase 2: Tenant Lifecycle & Reconciler"]
        T2_1["TASK-2.1 Post-Commit Event Decoupling"] --> T2_2["TASK-2.2 Reconciler State Machine"]
        T2_2 --> T2_3["TASK-2.3 Native Driver Resolution & Rollback"]
        T2_3 --> T2_4["TASK-2.4 Two-Tier Self-Healing Poller"]
        T2_4 --> T2_5["TASK-2.5 Tenant Decommissioning & Key Revocation"]
    end

    subgraph P3 ["Phase 3: Defaults & KB Protection"]
        T3_1["TASK-3.1 Atomic Conditional Defaults SQL"] --> T3_2["TASK-3.2 Explicit Intent Provenance"]
        T3_2 --> T3_3["TASK-3.3 3-Stage Embedding Lifecycle"]
        T3_3 --> T3_4["TASK-3.4 Dataset Immutability Guard"]
    end

    subgraph P4 ["Phase 4: Usage Metering & Reconciliation"]
        T4_1["TASK-4.1 Spend Log Staging & Deduplication"] --> T4_2["TASK-4.2 48-Hour Daily Usage Pipeline"]
        T4_2 --> T4_3["TASK-4.3 Self-Owned Model Zero Billing Audit"]
        T4_3 --> T4_4["TASK-4.4 Admin Usage & CSV Export API"]
    end

    subgraph P5 ["Phase 5: E2E Integration & QA"]
        T5_1["TASK-5.1 Test Cluster Orchestration"] --> T5_2["TASK-5.2 Native Driver E2E Integration"]
        T5_2 --> T5_3["TASK-5.3 Security, Resilience & Guard Tests"]
        T5_3 --> T5_4["TASK-5.4 Non-Invasive Audit & CI Gates"]
    end

    P0 --> P1
    P1 --> P2
    P2 --> P3
    P3 --> P4
    P4 --> P5
```

### 7.3 Detailed Engineering Task Specifications

#### Phase 0: Build & Overlay Foundation (Build & Overlay Foundation)

##### TASK-0.1: Affirmative Baseline Stubs (ee-baseline) Architecture & 19 Package Definitions
- **Description**: Provide community-equivalent fallback stubs for all 19 target packages excluded by global `ee` tags, excluding router symbols reserved for trampolines.
- **Inputs & Outputs**: Input: list of packages excluded by `!ee`; Output: 21 Go stub files in `overlay/ee-baseline/`.
- **Target Files**: `extensions/model-platform/overlay/ee-baseline/**/stub_ee.go`
- **Verification**: Baseline stubs provide all required non-router symbols and type definitions; individual baseline packages compile cleanly under `//go:build ee` without `build constraints exclude all Go files`.

##### TASK-0.2: Workspace Overlay Pipeline Script & Three Pre-Flight CI Assertions
- **Description**: Implement `apply-overlay.sh` to synchronize `ee-baseline`, `trampolines`, `backend`, and `web` layers into build targets with automated pre-flight assertions.
- **Inputs & Outputs**: Input: overlay directories; Output: `apply-overlay.sh` with exit code 0 on compliance.
- **Target Files**: `extensions/model-platform/scripts/apply-overlay.sh`
- **Verification**: Run `bash extensions/model-platform/scripts/apply-overlay.sh` and verify all assertions pass.

##### TASK-0.3: Thin Seam Trampolines & Backend API-Level Anti-Deletion Guard
- **Description**: Add thin trampolines in `internal/admin` and `internal/router` to inject routes and mount `ManagedProviderGuardMiddleware`.
- **Inputs & Outputs**: Input: Gin router groups; Output: route mounting and HTTP 403 on managed provider deletion.
- **Target Files**: `overlay/trampolines/**`, `backend/guard/managed_provider_guard.go`, `managed_provider_guard_test.go`
- **Verification**: Unit tests verify DELETE/PUT requests targeting managed providers return HTTP 403 Forbidden; full repository verification `go vet -tags "cgo,static,sonic,ee" ./...` compiles cleanly with zero undefined symbol errors once trampolines are mounted.

##### TASK-0.4: Frontend Web Overlay Seams & Enterprise Flag Defense
- **Description**: Connect frontend admin routes and navigation via `merge-routes.tsx` while strictly enforcing `VITE_RAGFLOW_ENTERPRISE=false`.
- **Inputs & Outputs**: Input: Web overlay seams; Output: model provider and usage statistics routes in admin panel.
- **Target Files**: `overlay/web/src/ee/merge-routes.tsx`, `navigation-private.tsx`, `model-providers.tsx`, `model-usage-statistics.tsx`
- **Verification**: `VITE_RAGFLOW_ENTERPRISE=false npm --prefix web run build` bundles cleanly without route crashes.

#### Phase 1: Gateway Infrastructure & Credential Pipeline (Gateway & Credential Pipeline)

##### TASK-1.1: LiteLLM Gateway Selection & Loopback Network Isolation
- **Description**: Configure LiteLLM Proxy container pinned to latest stable `v1.110.0` (image `ghcr.io/berriai/litellm:main-v1.110.0`) backed by PostgreSQL, binding strictly to loopback `127.0.0.1:4000` (Docker ports mapping `127.0.0.1:4000:4000`).
- **Inputs & Outputs**: Input: container definitions; Output: `gateway/docker-compose.yml`, `litellm-config.yaml`.
- **Target Files**: `extensions/model-platform/gateway/docker-compose.yml`, `litellm-config.yaml`, `validate-gateway-config.sh`
- **Verification**: `validate-gateway-config.sh` asserts loopback binding (`127.0.0.1`) and required configurations.

##### TASK-1.2: Root Provider Secret Physical Isolation & Log Masking
- **Description**: Isolate upstream API keys via `${PLATFORM_UPSTREAM_OPENAI_KEY}` and configure credential masking in LiteLLM logs.
- **Inputs & Outputs**: Input: environment variables; Output: masked logs and secret isolation test.
- **Target Files**: `gateway/litellm-config.yaml`, `gateway/test_secret_isolation.py`
- **Verification**: Run `test_secret_isolation.py` to assert root secrets never appear in logs or client responses.

##### TASK-1.3: Tenant Virtual Key Client & Timeout Controls
- **Description**: Implement Go client for `/key/generate`, `/key/delete`, and `/global/spend/logs` with 5-second HTTP timeouts and exponential retries.
- **Inputs & Outputs**: Input: gateway API specs; Output: `backend/gateway/client.go`, `types.go`, `client_test.go`.
- **Target Files**: `extensions/model-platform/backend/gateway/client.go`, `types.go`, `client_test.go`
- **Verification**: Unit tests assert virtual key generation, deletion, and timeout enforcement.

##### TASK-1.4: Gateway Permission Routing & Admin Endpoint Blocking
- **Description**: Verify tenant virtual keys cannot call unauthorized models or access management endpoints (`/key/*`).
- **Inputs & Outputs**: Input: virtual key calls; Output: HTTP 401/403 blockages.
- **Target Files**: `backend/gateway/permission_test.go`, `gateway/test_gateway_permissions.py`
- **Verification**: Automated test scripts assert 401/403 responses on unauthorized endpoint access.

#### Phase 2: Tenant Lifecycle & Reconciler (Tenant Lifecycle & Reconciler)

##### TASK-2.1: GORM Post-Commit Event Decoupling & Pre-Flight Checks
- **Description**: Decouple gateway network calls from upstream transactions using GORM `AfterCommit` events and pre-flight checks.
- **Inputs & Outputs**: Input: transaction commit events; Output: asynchronous dispatch on `event.EventBus`.
- **Target Files**: `backend/dao/tenant_hook.go`, `backend/event/bus.go`, `tx_hook.go`, `precheck.go`
- **Verification**: Unit tests assert events dispatch only after transaction commits successfully.

##### TASK-2.2: Reconciler State Machine, Optimistic Locking & Schema Migration
- **Description**: Create `platform_tenant_reconcile` table and implement state machine with optimistic concurrency locking.
- **Inputs & Outputs**: Input: state transition requests; Output: database schema migration and DAO methods.
- **Target Files**: `backend/entity/reconcile.go`, `backend/dao/reconcile.go`, `migration/001_reconcile_schema.go`
- **Verification**: Unit tests assert valid state transitions (`pending -> provisioning -> ready`) and lock conflict handling.

##### TASK-2.3: Native Driver Resolution & Compensating Rollback
- **Description**: Provision `OpenAI-API-Compatible@platform` records and implement compensating rollback (`POST /key/delete`) on DB failure.
- **Inputs & Outputs**: Input: tenant provisioning events; Output: native driver records and rollback cleanup.
- **Target Files**: `backend/reconciler/tenant_reconciler.go`, `tenant_reconciler_test.go`
- **Verification**: Tests assert native driver loading and immediate virtual key revocation if DB write fails.

##### TASK-2.4: Two-Tier Self-Healing Controller & Background Poller
- **Description**: Implement Tier 1 fast in-memory retry (5 attempts) and Tier 2 level-triggered background poller (`ReconcilePoller`).
- **Inputs & Outputs**: Input: degraded tenant records; Output: automated recovery to `ready` upon gateway return.
- **Target Files**: `backend/reconciler/poller.go`, `retry.go`, `poller_test.go`
- **Verification**: Tests simulate 45s gateway outage and assert poller restores all degraded tenants to `ready`.

##### TASK-2.5: Tenant Decommissioning Lifecycle Cleanup & Key Revocation
- **Description**: Capture tenant deletion events and invoke LiteLLM `POST /key/delete` to clean up virtual keys.
- **Inputs & Outputs**: Input: tenant deletion events; Output: physical key revocation from LiteLLM PostgreSQL.
- **Target Files**: `backend/reconciler/tenant_cleanup.go`, `tenant_cleanup_test.go`
- **Verification**: Tests assert virtual key is revoked and record is marked `deleted` upon tenant decommissioning.

#### Phase 3: Defaults & KB Vector Space Protection (Defaults & KB Protection)

##### TASK-3.1: Atomic Conditional SQL for Default Model Initializer
- **Description**: Initialize tenant default models using atomic conditional SQL (`WHERE (llm_id IS NULL OR llm_id = '')`).
- **Inputs & Outputs**: Input: default model assignments; Output: race-free user tenant updates.
- **Target Files**: `backend/reconciler/defaults_manager.go`, `defaults_manager_test.go`
- **Verification**: Concurrency tests assert parallel initializations never overwrite existing values.

##### TASK-3.2: Explicit Intent Provenance Tracking & Sticky Guard
- **Description**: Track explicit user choices in `platform_user_default_preference` to prevent platform upgrades from overwriting custom choices.
- **Inputs & Outputs**: Input: user model selections; Output: preference schema and sticky guard.
- **Target Files**: `backend/entity/preference.go`, `migration/002_preference_schema.go`, `reconciler/sticky_guard.go`
- **Verification**: Tests assert platform default upgrades preserve explicit user choices.

##### TASK-3.3: Three-Stage Embedding Lifecycle Protection
- **Description**: Implement 3-tier lifecycle (`active -> deprecated -> tombstoned`) for embedding models.
- **Inputs & Outputs**: Input: model deprecation requests; Output: read-only whitelist retention for existing KBs.
- **Target Files**: `backend/reconciler/embedding_lifecycle.go`, `embedding_lifecycle_test.go`
- **Verification**: Tests assert deprecated models remain searchable for existing KBs but hidden from new KB creation.

##### TASK-3.4: Dataset Vector Space Immutability Guard
- **Description**: Implement `DatasetImmutabilityGuardMiddleware` blocking embedding model mutation on parsed datasets with chunks.
- **Inputs & Outputs**: Input: dataset update requests; Output: HTTP 400 Bad Request on embedding mutation.
- **Target Files**: `backend/guard/dataset_immutability_guard.go`, `dataset_immutability_guard_test.go`
- **Verification**: Tests assert mutating embedding model on datasets with chunks returns HTTP 400.

#### Phase 4: Usage Metering & Reconciliation Pipeline (Usage Metering & Reconciliation)

##### TASK-4.1: Gateway Spend Log Staging & Deduplication
- **Description**: Collect atomic spend logs into `platform_gateway_spend_log` using `INSERT IGNORE` on `spend_id`.
- **Inputs & Outputs**: Input: LiteLLM spend logs; Output: deduplicated database records.
- **Target Files**: `backend/entity/spend_log.go`, `migration/003_spend_log_schema.go`, `metering/collector.go`
- **Verification**: Tests assert duplicate spend log inputs produce zero duplicate rows.

##### TASK-4.2: 48-Hour Daily Usage Pipeline with Replacement Upsert
- **Description**: Aggregate daily usage into `platform_tenant_usage_daily` over a day-aligned 48h rolling window using replacement upsert.
- **Inputs & Outputs**: Input: staged spend logs; Output: idempotent daily usage records.
- **Target Files**: `backend/entity/usage.go`, `migration/004_usage_schema.go`, `metering/pipeline.go`
- **Verification**: Running pipeline multiple times over the same window produces identical usage totals.

##### TASK-4.3: Self-Owned Model Zero Billing Physical Isolation Audit
- **Description**: Verify that tenant self-owned models bypass the gateway completely and generate zero platform billing.
- **Inputs & Outputs**: Input: self-owned model calls; Output: 0 gateway spend logs and $0.00 platform spend.
- **Target Files**: `backend/metering/isolation_audit.go`, `isolation_audit_test.go`
- **Verification**: Audit tests verify zero gateway spend logs generated during direct provider calls.

##### TASK-4.4: Admin Usage Statistics & Streaming CSV Export API
- **Description**: Implement paginated daily usage query and streaming CSV export endpoints for administrators.
- **Inputs & Outputs**: Input: admin query parameters; Output: paginated JSON usage summaries and CSV file streams.
- **Target Files**: `backend/admin/usage_handler.go`, `usage_handler_test.go`
- **Verification**: Unit tests verify JSON response formatting and streaming CSV row serialization.

#### Phase 5: End-to-End Integration & Verification QA (E2E Integration & Verification QA)

##### TASK-5.1: Isolated Test Cluster Orchestration & Network Isolation
- **Description**: Orchestrate PostgreSQL and LiteLLM test containers with healthcheck probes and isolated network bridges.
- **Inputs & Outputs**: Input: test compose configuration; Output: running test cluster on `127.0.0.1:4000`.
- **Target Files**: `tests/e2e/docker-compose.test.yml`, `tests/e2e/setup_env.sh`
- **Verification**: Run `setup_env.sh` and verify containers reach healthy state on `127.0.0.1:4000`.

##### TASK-5.2: Native Driver End-to-End Integration Tests
- **Description**: Implement end-to-end integration tests validating Chat, Embedding, defaults, and spend log rollups against the live cluster.
- **Inputs & Outputs**: Input: live gateway and database; Output: verified end-to-end integration test suite.
- **Target Files**: `tests/e2e/e2e_core_test.go`
- **Verification**: `go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestE2E"` passes 100%.

##### TASK-5.3: Security, Stability & Guard End-to-End Test Suite
- **Description**: Implement E2E security tests verifying 403 provider deletion guard, 400 dataset immutability, and 45s outage recovery.
- **Inputs & Outputs**: Input: security attack simulations; Output: verified protection and resilience.
- **Target Files**: `tests/e2e/e2e_security_stability_test.go`
- **Verification**: `go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestSecurityStability"` passes 100%.

##### TASK-5.4: Non-Invasive Audit & Automated CI Gates
- **Description**: Implement `audit-non-invasive.sh` and GitHub Actions CI workflow running all gates and tests automatically.
- **Inputs & Outputs**: Input: CI environment; Output: automated gates and PR verification workflow.
- **Target Files**: `scripts/audit-non-invasive.sh`, `.github/workflows/ci.yml`
- **Verification**: `bash extensions/model-platform/scripts/audit-non-invasive.sh` passes all 3 gates with Exit Code 0.

---

## 8. End-to-End Integration & Verification Guide (Phase 0~5 Core MVP)

### 8.1 Target & Scope Definition (Core Capability MVP)
The initial delivery target (tracked within the test suite harness as `tests/e2e`) establishes a minimal, robust, end-to-end verifiable multi-tenant model hosting capability:
- **Models**: Chat (`gpt-4o-mini`) and Embedding (`text-embedding-3-small`) resolved via native `OpenAI-API-Compatible` driver.
- **Boundary Proofs**: Zero changes to `upstream/ragflow`, enterprise UI flag disabled, 19 packages stubbed.
- **Lifecycle & Metering**: Automated tenant provisioning, two-tier self-healing, atomic defaults, dataset immutability guard, spend log deduplication, 48h idempotent rollup, and zero-spend self-owned model bypass.

### 8.2 End-to-End Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Admin as Platform Admin
    actor User as Tenant User
    participant Web as Web Overlay
    participant Core as RAGFlow Core
    participant DB as RAGFlow DB
    participant Reconciler as Model Reconciler
    participant Gateway as LiteLLM Proxy (127.0.0.1:4000)
    participant Provider as External Provider

    Note over Admin,Provider: Step 1: Gateway Initialization & Model Configuration
    Admin->>Gateway: Deploy proxy with ${PLATFORM_UPSTREAM_OPENAI_KEY}
    Gateway-->>Admin: Pinned healthcheck liveliness OK (HTTP 200)

    Note over User,Provider: Step 2: Tenant Registration & Event Decoupling
    User->>Core: Register new tenant
    Core->>DB: Begin transaction & insert user_tenant
    DB-->>Core: Transaction committed successfully
    Core->>Reconciler: Dispatch TenantCreatedEvent via GORM AfterCommit
    Reconciler->>Gateway: POST /key/generate with Master Key
    Gateway-->>Reconciler: Issue scoped virtual key (sk-tenant-xxx)
    Reconciler->>DB: Persist managed records (OpenAI-API-Compatible@platform)
    Reconciler->>DB: Atomic SQL default init & set status='ready'

    Note over User,Provider: Step 3: Native Driver Inference
    User->>Core: Chat completion request
    Core->>Gateway: Forward inference via 127.0.0.1:4000 with sk-tenant-xxx
    Gateway->>Provider: Proxy to upstream provider with root secret
    Provider-->>Gateway: Stream tokens
    Gateway-->>Core: Return completion
    Core-->>User: Present response

    Note over Admin,Provider: Step 4: Metering & Idempotent Reconciliation
    Gateway->>Gateway: Write atomic Spend Log to PostgreSQL
    Reconciler->>Gateway: GET /global/spend/logs
    Gateway-->>Reconciler: Return spend records
    Reconciler->>DB: INSERT IGNORE into platform_gateway_spend_log
    Reconciler->>DB: 48h calendar-aligned replacement upsert into usage_daily
    Admin->>Core: GET /api/v1/admin/model-platform/usage/export
    Core-->>Admin: Stream reconciled billing CSV
```

### 8.3 Failure Modes & Fault Tolerance Matrix

| Failure Scenario | Impact Scope | Defense & Recovery Strategy |
|---|---|---|
| Gateway down during tenant registration | Delayed model provisioning | Tenant registration commits locally without blocking. Event enqueues for retry; Tier 1 retries 5 times, then transitions to `degraded`. Tier 2 background poller auto-heals tenant to `ready` once gateway returns. |
| Database failure after virtual key issued | Orphaned gateway credentials | Compensating rollback invokes `POST /key/delete` to revoke virtual key immediately, preventing orphaned tokens. |
| Tenant account deleted or decommissioned | Residual token usage | Decommissioning listener triggers `TenantDecommissioner`, invoking `POST /key/delete` to revoke token physically. |
| Upstream provider 5xx or network timeout | Active inference fails | LiteLLM performs transparent retry or returns standard error codes. RAGFlow driver handles errors gracefully; gateway charges only for actual tokens consumed. |
| Administrator deprecates embedding model | Existing KB vector search | 3-stage lifecycle keeps model in `Deprecated/Read-Only` status. Gateway whitelist retains model, ensuring vector search continues uninterrupted while hiding it from new KBs. |
| Network delay causes 48h rollup replay | Billing aggregates | Raw staging table uses `spend_id` unique key (`INSERT IGNORE`). Rollup performs replacement upsert (`call_count = VALUES(call_count)`), guaranteeing mathematical idempotence. |
| Tenant attempts to delete managed provider | Service corruption | `ManagedProviderGuardMiddleware` intercepts request and returns HTTP 403 Forbidden. |

### 8.4 Step-by-Step Execution & Verification Commands

#### Step 1: Workspace Overlay Sync & Clean Build
```bash
# 1. Prepare clean build target
rm -rf build/ragflow && mkdir -p build/ragflow
git -C upstream/ragflow archive HEAD | tar -x -C build/ragflow

# 2. Apply non-invasive overlay and baseline stubs
bash extensions/model-platform/scripts/apply-overlay.sh

# 3. Verify upstream submodule remains 100% clean
git -C upstream/ragflow status --porcelain

# 4. Compile Go backend with EE build tags
cd build/ragflow
RAGFLOW_GO_TAGS=ee bash build.sh --go

# 5. Bundle frontend under disabled enterprise flag
cd web
VITE_RAGFLOW_ENTERPRISE=false npm run build
cd ../../..
```

#### Step 2: Start Test Cluster & Check Isolation
```bash
# Start PostgreSQL and LiteLLM test cluster (loopback bound to 127.0.0.1:4000)
docker compose -f extensions/model-platform/tests/e2e/docker-compose.test.yml up -d

# Verify gateway liveliness probe
curl -s -f http://127.0.0.1:4000/health/liveliness || exit 1
```

#### Step 3: Issue Scoped Tenant Virtual Key
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

#### Step 4: Security, Permission & Guard Verification
```bash
# 1. Verify managed provider anti-deletion guard (Expect 403 Forbidden)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "http://localhost:8080/v1/user/setting/model/provider?name=OpenAI-API-Compatible" \
  -H "Authorization: Bearer <tenant_user_token>")
echo "Managed Provider Anti-Deletion Guard (Expect 403): $STATUS_CODE"

# 2. Verify management endpoint blocking with tenant key (Expect 401 or 403)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "http://127.0.0.1:4000/key/generate" \
  -H "Authorization: Bearer $TENANT_VIRTUAL_KEY" \
  -H "Content-Type: application/json" \
  -d '{"models": ["gpt-4o"]}')
echo "Admin Endpoint Block Check (Expect 401 or 403): $STATUS_CODE"

# 3. Verify unauthorized model blocking (Expect 403 Forbidden)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "http://127.0.0.1:4000/v1/chat/completions" \
  -H "Authorization: Bearer $TENANT_VIRTUAL_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "unauthorized-model", "messages": [{"role":"user","content":"hi"}]}')
echo "Unauthorized Model Block Check (Expect 403): $STATUS_CODE"

# 4. Verify dataset vector space immutability guard (Expect 400 Bad Request)
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT \
  "http://localhost:8080/v1/dataset/ds-e2e-test" \
  -H "Authorization: Bearer <tenant_user_token>" \
  -H "Content-Type: application/json" \
  -d '{"embedding_model": "modified-model"}')
echo "Dataset Immutability Guard (Expect 400): $STATUS_CODE"
```

#### Step 5: Spend Log Staging & Idempotent Rollup
```bash
# 1. Query LiteLLM authoritative spend logs
curl -s -X GET "http://127.0.0.1:4000/global/spend/logs" \
  -H "Authorization: Bearer sk-master-e2e-platform-test-key" | jq .

# 2. Query aggregated daily usage via Admin API
curl -s -X GET "http://localhost:8080/api/v1/admin/model-platform/usage?tenant_id=tenant-e2e-core" \
  -H "Authorization: Bearer <admin_token>" | jq .
```

#### Step 6: Native Driver Go E2E Integration Test Suite
```bash
# Run core E2E integration suite (Native driver, live cluster, defaults, lifecycle, rollup, zero-spend audit)
go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestE2E"

# Run security, resilience, and middleware guard test suite
go test -v -tags "ee" ./extensions/model-platform/tests/e2e -run "TestSecurityStability"
```

#### Step 7: Automated Non-Invasive Audit & CI Gates
```bash
# Execute automated non-invasive compliance audit
bash extensions/model-platform/scripts/audit-non-invasive.sh
```
