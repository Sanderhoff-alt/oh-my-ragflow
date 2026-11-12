# RAGFlow 扩展点

[English](extension-points.md) | 简体中文

本文记录固定的 RAGFlow `v1.0.0` submodule 中观察到的扩展机制，以及 oh-my-ragflow 使用这些机制的规范。本文说明集成边界，不代表所有扩展点都已针对某项功能验证；升级上游时应重新检查源码和调用方。

## 集成规范

- 保持上游 submodule 不变。在生成的构建工作区应用集成文件和必要适配；不要通过修改上游共享内核文件实现扩展功能。
- 优先使用明确提供的扩展接缝和受支持的 API。使用前检查调用方、执行上下文、授权和生命周期。
- 在目标扩展范围之外保留上游社区版行为和公开契约。上游桩可能包含有意义的行为，并非都是空实现。
- 固定并校验上游版本和相关输入文件。预期接缝变化或无法安全应用适配时，明确报错。
- 构建并测试增强发行版，覆盖扩展和未改变的社区版行为；单独说明未运行或排除的上游测试。
- 不假定所有接缝都可组合：对重叠实现和不支持的组合明确报错，避免静默覆盖。多扩展选择与组合是 [issue #2](https://github.com/Sanderhoff-alt/oh-my-ragflow/issues/2) 跟踪的未来工作。

## Go 后端

### 构建标签与社区版实现

上游使用 `//go:build !ee` 等 Go 构建约束提供社区版实现，使用 `//go:build ee` 选择企业版实现。[`build.sh`](../upstream/ragflow/build.sh) 将 `RAGFLOW_GO_TAGS` 附加到构建与测试标签。设置 `RAGFLOW_GO_TAGS=ee` 是全局构建选择：在参与构建的包中，所有匹配的 `!ee` 文件都会被排除，而不只是扩展需要改变的单个函数。社区版检出不包含完整企业版实现。

因此，使用 `ee` 集成时，必须提供被标签排除且构建所需的符号。无关函数、类型和方法应保留原来的社区版行为，只实现目标差异，并避免重复定义。如何生成和维护这份基础适配尚未确定或实现。需要对照固定版本源码验证覆盖范围，包括不叫 `stub.go` 的 `!ee` 文件和仅供测试使用的定义。

固定版本中的代表性接缝如下：

| 范围 | 社区版实现 | 示例；使用前需检查调用方 |
| --- | --- | --- |
| 管理端 | [`internal/admin/stub.go`](../upstream/ragflow/internal/admin/stub.go) | 路由注册、心跳与服务状态、许可证行为 |
| API 路由 | [`internal/router/stub.go`](../upstream/ragflow/internal/router/stub.go) | 路由注册和功能中间件 |
| 租户和身份 | [`internal/service/stub.go`](../upstream/ragflow/internal/service/stub.go)、[`internal/dao/stub.go`](../upstream/ragflow/internal/dao/stub.go) | 租户解析、权限、租户创建钩子 |
| 计费与权限 | [`internal/service/billing/stub.go`](../upstream/ragflow/internal/service/billing/stub.go)、[`internal/permission/stub.go`](../upstream/ragflow/internal/permission/stub.go) | 计费服务和权限策略接缝 |
| 其他包 | [`cmd`](../upstream/ragflow/cmd)、[`internal`](../upstream/ragflow/internal) | 其他带标签文件和定义；本表并不穷尽全部入口 |

例如，租户 DAO 在事务中、插入租户记录之前调用创建钩子。这个钩子本身不能证明具备安全的事务提交后生命周期，也不能据此认定可安全执行远程副作用。模型平台的具体待验证问题见其[架构文档](../extensions/model-platform/architecture.zh-CN.md)。

上游构建脚本还会按需加载 `build_ee.sh`，并提供受条件保护的企业版原生集成测试钩子。它们是构建集成接缝，不能证明存在任意所需的运行时钩子；使用前须核对具体行为。

### 测试和升级检查

根据需要分别构建社区版基线和增强版。`ee` 标签同样会排除 `!ee` 测试；编译成功或增强版测试通过，不能单独证明社区版行为仍受到覆盖。应识别并明确验证等效测试覆盖。变更固定版本时，核对构建约束、签名、调用方、非平凡的回退行为和生成的适配。

## React 前端

### 企业版覆盖文件

上游从 [`web/src/ee/`](../upstream/ragflow/web/src/ee) 导入明确的企业版接缝。社区版源码提供兼容实现，企业版构建在相同路径替换选定文件。与 Go 的全局标签不同，替换一个前端文件不会自动替换其他文件。

只在生成的构建工作区替换功能所需的上游接缝，并保留其导出、类型和必要的社区版行为。未更改的 `ee` 文件继续使用上游社区版版本。例如：

| 范围 | 上游接缝或调用方 |
| --- | --- |
| 应用路由 | [`ee/merge-routes.tsx`](../upstream/ragflow/web/src/ee/merge-routes.tsx)、[`ee/private-routes.tsx`](../upstream/ragflow/web/src/ee/private-routes.tsx) |
| 管理端导航 | [`ee/pages/admin/layouts/navigation-private.tsx`](../upstream/ragflow/web/src/ee/pages/admin/layouts/navigation-private.tsx) |
| 租户设置菜单 | [`ee/pages/user-setting/sidebar/use-setting-menu.tsx`](../upstream/ragflow/web/src/ee/pages/user-setting/sidebar/use-setting-menu.tsx) |
| API 地址 | [`ee/utils/api-private.ts`](../upstream/ragflow/web/src/ee/utils/api-private.ts)，由 [`utils/api.ts`](../upstream/ragflow/web/src/utils/api.ts) 合并 |
| 国际化 | [`ee/locales`](../upstream/ragflow/web/src/ee/locales)，由 [`locales/config.ts`](../upstream/ragflow/web/src/locales/config.ts) 合并 |
| 组件、守卫和 hooks | [`web/src/ee/`](../upstream/ragflow/web/src/ee) 中对应文件及其共享调用方 |

本表只是示例清单，并不授权任意替换共享组件，也不保证某个接缝能支持目标功能。尤其要注意，社区版接缝可能不是空文件，可能导出调用方所需的类型、常量或行为。

### 企业版环境变量

固定版本的前端还在 [`pages/admin/utils.tsx`](../upstream/ragflow/web/src/pages/admin/utils.tsx) 中，根据 `VITE_RAGFLOW_ENTERPRISE=RAGFLOW_ENTERPRISE` 定义 `IS_ENTERPRISE`。部分共享管理端路由和界面分支使用此标志启用上游企业版功能。它的影响范围比单个覆盖文件更大，也不会补齐缺失的企业版代码或后端 API。

默认保持此标志关闭，优先使用针对具体功能的扩展接缝。如果扩展确实无法在不启用此标志的情况下满足需求，应先检查所有受影响的路径、授权和依赖，并在启用前记录理由与验证结果。不能将已启用的分支视为完整或受支持的企业版功能。

## 文档范围

这些是上游区分版本的接缝，不是稳定的外部插件 API。接缝存在不代表仅凭它们就能实现租户生命周期、模型归因、计费等任意功能。如果现有接缝或 API 不足，应记录限制并评估其他方式，不应宣称已能非侵入实现。具体扩展的决策和验证放在该扩展自己的文档中。
