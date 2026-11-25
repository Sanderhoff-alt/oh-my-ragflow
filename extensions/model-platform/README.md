# Model Platform Extension

English | [简体中文](README.zh-CN.md)

The `model-platform` extension provides centrally managed, tenant-scoped platform models for RAGFlow through an isolated LiteLLM gateway while preserving every tenant's freedom to configure Bring Your Own Key (BYOK) providers. Platform model consumption is attributed and metered authoritatively at the gateway; tenant-owned BYOK traffic bypasses the gateway physically and incurs `$0.00` platform spend.

**Status:** Production Ready (Dynamic Model Catalog, Tenant Lifecycle & Self-Healing, Emergency Key Rotation, Authoritative Metering & Audit, and Admin Governance UI).

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

1. **Configure and start the isolated LiteLLM gateway**:

   First create `.env` (without overwriting an existing one) and populate real secrets for `POSTGRES_PASSWORD`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, and `PLATFORM_UPSTREAM_OPENAI_KEY` (if `POSTGRES_PASSWORD` contains URL-reserved characters such as `@`, `#`, `:`, `/`, or `%`, set an explicit percent-encoded `DATABASE_URL` as well):

   ```sh
   [ -f extensions/model-platform/gateway/.env ] || cp extensions/model-platform/gateway/.env.example extensions/model-platform/gateway/.env
   chmod 600 extensions/model-platform/gateway/.env
   # Edit extensions/model-platform/gateway/.env and replace all example secrets before continuing
   ```

   Then validate the configuration and start the gateway in a subshell (so your shell stays at the repository root, and startup aborts if validation fails):

   ```sh
   (cd extensions/model-platform/gateway && ./validate-gateway-config.sh && docker compose up -d)
   ```

2. **Assemble and build the enhanced RAGFlow distribution** from the repository root (keeping `upstream/ragflow` 100% untouched):

   ```sh
   ./extensions/model-platform/scripts/apply-overlay.sh \
     && (cd build/ragflow && RAGFLOW_GO_TAGS=ee bash build.sh --go) \
     && (cd build/ragflow/web && npm ci && VITE_RAGFLOW_ENTERPRISE=false npm run build)
   ```

3. **Configure RAGFlow API & Admin environment variables** (enter `LITELLM_MASTER_KEY` verbatim as set in `.env`, using single quotes so the shell does not expand `$` or other special characters):

   ```sh
   export LITELLM_BASE_URL='http://127.0.0.1:4000'
   export LITELLM_MASTER_KEY='replace-with-gateway-master-key'
   ```

   Start the compiled RAGFlow API and Admin services from `build/ragflow` with these environment variables set. When adding a model route to `extensions/model-platform/gateway/litellm-config.yaml`, re-validate the config before recreating the proxy container (`(cd extensions/model-platform/gateway && ./validate-gateway-config.sh && docker compose up -d --force-recreate --no-deps litellm-proxy)`) so invalid YAML cannot disrupt the running gateway and LiteLLM mounts any atomically replaced config file, then onboard the model from **Admin Console → Model Providers** (`/admin/model-providers`).

## Documentation

| Audience | English | 简体中文 | Scope |
| :--- | :--- | :--- | :--- |
| **Platform Operators & Superusers** | [Admin & Operator Guide](docs/admin-guide.md) | [管理员与运维指南](docs/admin-guide.zh-CN.md) | Network isolation, Docker deployment, environment variables, Admin Console UI walkthroughs, batch key rotation, and disaster recovery |
| **Tenant Owners, Admins & End Users** | [Tenant & User Guide](docs/user-guide.md) | [租户与用户指南](docs/user-guide.zh-CN.md) | Role permissions (`owner` / `admin` / `normal`), platform models vs. BYOK, UI walkthroughs, sticky defaults, dataset embedding immutability, and FAQ |
| **Integrators & Automation Developers** | [API Reference](docs/api-reference.md) | [API 参考文档](docs/api-reference.zh-CN.md) | Complete specification for all 13 Admin API endpoints plus App API middleware guard contracts and error codes |
| **Architects & Contributors** | [Architecture Design](architecture.md) | [架构设计](architecture.zh-CN.md) | System topology, core invariants, and Architecture Decision Records (ADR 1–8) |
| **Contributors** | [Engineering Design Reference](implementation-plan.md) | [工程设计参考](implementation-plan.zh-CN.md) | Subsystem internals, overlay seams, database schemas, fault-tolerance matrix, and verification commands |
| **Repository Contributors** | [RAGFlow Extension Points](../../docs/extension-points.md) | [RAGFlow 扩展点与集成规则](../../docs/extension-points.zh-CN.md) | Shared non-invasive integration rules and upstream seams |
