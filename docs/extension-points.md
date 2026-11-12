# RAGFlow extension points

English | [简体中文](extension-points.zh-CN.md)

This document describes the extension mechanisms observed in the pinned RAGFlow `v1.0.0` submodule and the rules for using them in oh-my-ragflow. It is a guide to integration boundaries, not a claim that every extension point has been validated for a particular feature. Reinspect the source and callers when upgrading upstream.

## Integration rules

- Keep the upstream submodule unchanged. Apply integration files and any necessary adaptations in a generated build workspace; do not patch shared upstream kernel files as a feature implementation.
- Prefer explicitly provided extension seams and supported APIs. Verify their callers, execution context, authorization, and lifecycle before relying on them.
- Preserve upstream community behavior and public contracts outside the intended extension. An upstream stub can implement meaningful behavior, not just return an empty value.
- Pin and verify the upstream version and relevant input files. Fail clearly if an expected seam changes or an integration cannot be applied safely.
- Build and test the enhanced distribution, including the extension and unaffected community behavior. State separately which upstream tests were excluded or not run.
- Do not assume every seam composes: detect overlapping implementations and unsupported combinations rather than silently overwriting them. Multi-extension selection and composition are future work tracked in [issue #2](https://github.com/Sanderhoff-alt/oh-my-ragflow/issues/2).

## Go backend

### Build tags and community implementations

Upstream uses Go build constraints such as `//go:build !ee` for community implementations and `//go:build ee` for enterprise-side implementations. [`build.sh`](../upstream/ragflow/build.sh) appends `RAGFLOW_GO_TAGS` to build and test tags. Setting `RAGFLOW_GO_TAGS=ee` is a global build choice: it excludes all matching `!ee` files in selected packages, not just the one function the extension needs. The community checkout does not contain the full enterprise implementation.

Consequently, an `ee` integration must provide the required symbols excluded by the tag. Preserve the original community behavior for unrelated functions, types, and methods; implement only the intended differences. Avoid duplicate definitions. How to produce and maintain this baseline adaptation is not yet decided or implemented. Verify its coverage against the pinned sources, including `!ee` files that are not named `stub.go` and test-only definitions.

Representative seams in the pinned source include:

| Area | Community implementation | Examples; inspect callers before use |
| --- | --- | --- |
| Admin | [`internal/admin/stub.go`](../upstream/ragflow/internal/admin/stub.go) | Router registration, heartbeat and service status, license behavior |
| API routing | [`internal/router/stub.go`](../upstream/ragflow/internal/router/stub.go) | Route registration and feature middleware |
| Tenant and identity | [`internal/service/stub.go`](../upstream/ragflow/internal/service/stub.go), [`internal/dao/stub.go`](../upstream/ragflow/internal/dao/stub.go) | Tenant resolution, permissions, tenant-creation hooks |
| Billing and permissions | [`internal/service/billing/stub.go`](../upstream/ragflow/internal/service/billing/stub.go), [`internal/permission/stub.go`](../upstream/ragflow/internal/permission/stub.go) | Billing service and permission-policy seams |
| Other packages | [`cmd`](../upstream/ragflow/cmd), [`internal`](../upstream/ragflow/internal) | Additional tagged files and definitions; this table is not exhaustive |

For example, the tenant DAO calls its creation hook inside a transaction before inserting the tenant. That hook alone does not establish a safe post-commit lifecycle or justify remote side effects. The model-platform-specific questions are documented in its [architecture](../extensions/model-platform/architecture.md).

The upstream build script also optionally sources `build_ee.sh` and has a guarded enterprise native-integration-test hook. These are build integration seams, not evidence that arbitrary runtime hooks exist. Verify their exact behavior before using them.

### Tests and upgrade checks

Build both the community baseline and the enhanced configuration where relevant. The `ee` tag also excludes `!ee` tests; successful compilation or a green enhanced run alone does not prove community behavior remains covered. Identify and test equivalent coverage explicitly. Validate build constraints, signatures, callers, nontrivial fallback behavior, and generated adaptations when changing the pinned version.

## React frontend

### Enterprise overlay files

Upstream imports explicit enterprise-side seams from [`web/src/ee/`](../upstream/ragflow/web/src/ee). The community tree provides compatible implementations; the enterprise build replaces selected files at the same paths. Unlike Go's global tag, replacing one frontend file does not automatically replace all other files.

Only replace the specific upstream seam needed by the feature in the generated workspace. Preserve its exports, types, and required community behavior. Unchanged `ee` files remain the upstream community versions. Examples include:

| Area | Upstream seam or caller |
| --- | --- |
| Application routes | [`ee/merge-routes.tsx`](../upstream/ragflow/web/src/ee/merge-routes.tsx), [`ee/private-routes.tsx`](../upstream/ragflow/web/src/ee/private-routes.tsx) |
| Admin navigation | [`ee/pages/admin/layouts/navigation-private.tsx`](../upstream/ragflow/web/src/ee/pages/admin/layouts/navigation-private.tsx) |
| Tenant settings menu | [`ee/pages/user-setting/sidebar/use-setting-menu.tsx`](../upstream/ragflow/web/src/ee/pages/user-setting/sidebar/use-setting-menu.tsx) |
| API endpoints | [`ee/utils/api-private.ts`](../upstream/ragflow/web/src/ee/utils/api-private.ts), merged by [`utils/api.ts`](../upstream/ragflow/web/src/utils/api.ts) |
| Translations | [`ee/locales`](../upstream/ragflow/web/src/ee/locales), merged by [`locales/config.ts`](../upstream/ragflow/web/src/locales/config.ts) |
| Components, guards and hooks | The corresponding files under [`web/src/ee/`](../upstream/ragflow/web/src/ee) and their shared callers |

This is an example inventory, not permission to replace arbitrary shared components or a guarantee that a seam supports a feature. In particular, a nonempty community seam may export types, constants, or behavior that callers require.

### Enterprise environment flag

The pinned frontend also defines `IS_ENTERPRISE` from `VITE_RAGFLOW_ENTERPRISE=RAGFLOW_ENTERPRISE` in [`pages/admin/utils.tsx`](../upstream/ragflow/web/src/pages/admin/utils.tsx). Some shared admin routes and UI branches use this flag for upstream enterprise features. It is broader than an individual overlay and does not supply missing enterprise code or backend APIs.

Leave this flag disabled by default and prefer the specific extension seams. If an extension cannot meet its requirements without the flag, first inspect every affected path, authorization check, and dependency, and document the rationale and validation before enabling it. Do not treat an enabled branch as a complete or supported enterprise feature.

## Scope of this guide

These are upstream edition seams, not a stable external plugin API. Their presence does not guarantee tenant lifecycle, model attribution, billing, or any other feature can be implemented through them alone. If the existing seam or API is insufficient, record the limitation and evaluate alternatives rather than claiming a non-invasive implementation works. Extension-specific decisions and validation belong in that extension's documentation.
