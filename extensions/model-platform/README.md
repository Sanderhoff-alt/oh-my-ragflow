# Model Platform Extension

English | [简体中文](README.zh-CN.md)

The `model-platform` extension provides centrally managed, tenant-scoped platform models for RAGFlow through an isolated LiteLLM gateway while preserving every tenant's freedom to configure Bring Your Own Key (BYOK) providers. Platform model consumption is attributed and metered authoritatively at the gateway; tenant-owned BYOK traffic bypasses the gateway physically and incurs `$0.00` platform spend.

**Status:** Production Ready (Phases 0–5 Completed & Merged, including Dynamic Model Catalog, Emergency Key Rotation, Self-Healing Metering, and Admin Governance UI).

## Feature Highlights

- **Physical Credential Isolation**: Root upstream provider keys stay inside a loopback-bound LiteLLM gateway (`127.0.0.1:4000`). Each tenant receives a scoped virtual key (`sk-tenant-*`) stored on its managed `platform` instance.
- **7 Managed Model Types**: Supports `chat` (1), `embedding` (2), `asr` (4), `vision` (8), `rerank` (16), `tts` (32), and `decision` (128). Local `ocr` (64) pipelines remain outside the gateway.
- **Dynamic Catalog & Fan-Out Rollouts**: Administrators onboard, retire, reactivate, and switch platform default models from the Admin Console or Admin API with asynchronous per-tenant convergence, progress tracking, and failed-tenant retry.
- **Tenant Protection & Zero-Billing BYOK**:
  - Frontend (`ProviderInstanceCardWithPrivilege`) and backend (`ManagedProviderGuardMiddleware`) prevent tenants from modifying or deleting managed `platform` instances.
  - `DefaultModelPreferenceMiddleware` + `StickyGuard` preserve explicit user model choices across platform default upgrades.
  - `DatasetImmutabilityGuardMiddleware` freezes embedding models on non-empty datasets and blocks binding retired embeddings to new or empty datasets while preserving read-only retrieval for existing datasets.
  - Self-owned BYOK providers connect directly to external endpoints (`$0.00` platform spend).
- **Authoritative Metering & Isolation Audit**: Incremental watermark ingestion, idempotent calendar-aligned daily rollups (prompt/completion/cached/reasoning tokens, TTS characters, spend, and cache savings), automatic multi-day catch-up, opt-in retention sweeps, and live shadow-spend isolation audits.
- **Zero-Downtime Emergency Key Rotation**: Single-tenant rotation from Admin UI/API plus rate-limited batch rotation via `scripts/emergency-rotate-keys.sh` with automatic background revocation of superseded keys.

## Quick Start

1. **Start the isolated LiteLLM gateway**:

   ```sh
   cd extensions/model-platform/gateway
   cp .env.example .env
   # Populate POSTGRES_PASSWORD, LITELLM_MASTER_KEY, LITELLM_SALT_KEY, and upstream provider keys
   ./validate-gateway-config.sh
   docker compose up -d
   ```

2. **Assemble and build the enhanced RAGFlow distribution** (keeping `upstream/ragflow` 100% untouched):

   ```sh
   ./extensions/model-platform/scripts/apply-overlay.sh
   ```

3. **Configure RAGFlow API & Admin environment variables**:

   ```sh
   export LITELLM_BASE_URL=http://127.0.0.1:4000
   export LITELLM_MASTER_KEY=<same-master-key-as-gateway>
   ```

   Start the enhanced RAGFlow API and Admin services, then add routes to `gateway/litellm-config.yaml` and onboard models from **Admin Console → Model Providers** (`/admin/model-providers`).

## Documentation

| Audience | English | 简体中文 | Scope |
| :--- | :--- | :--- | :--- |
| **Platform Operators & Superusers** | [Admin & Operator Guide](docs/admin-guide.md) | [管理员与运维指南](docs/admin-guide.zh-CN.md) | Network isolation, Docker deployment, environment variables, Admin Console UI walkthroughs, batch key rotation, and disaster recovery |
| **Tenant Owners, Admins & End Users** | [Tenant & User Guide](docs/user-guide.md) | [租户与用户指南](docs/user-guide.zh-CN.md) | Role permissions (`owner` / `admin` / `normal`), platform models vs. BYOK, UI walkthroughs, sticky defaults, dataset embedding immutability, and FAQ |
| **Integrators & Automation Developers** | [API Reference](docs/api-reference.md) | [API 参考文档](docs/api-reference.zh-CN.md) | Complete specification for all 13 Admin API endpoints plus App API middleware guard contracts and error codes |
| **Architects & Contributors** | [Architecture Design](architecture.md) | [架构设计](architecture.zh-CN.md) | System topology, core invariants, and Architecture Decision Records (ADR 1–6) |
| **Contributors** | [Implementation Plan](implementation-plan.md) | [工程实施计划](implementation-plan.zh-CN.md) | Phase 0–5 engineering roadmap, schema migrations, and E2E verification suite |
| **Repository Contributors** | [RAGFlow Extension Points](../../docs/extension-points.md) | [RAGFlow 扩展点与集成规则](../../docs/extension-points.zh-CN.md) | Shared non-invasive integration rules and upstream seams |
