# oh-my-ragflow

English | [简体中文](README.zh-CN.md)

An independent extension project for RAGFlow. It adds capabilities around RAGFlow while keeping the upstream kernel unchanged.

## Status

The project is in the design stage. See each extension's README for its scope and implementation status.

## Baseline

- RAGFlow: [`v1.0.0`](https://github.com/infiniflow/ragflow/releases/tag/v1.0.0), commit `db1cd5929a7885a3074547791f5ecc4d2f891739`.
- The baseline is tracked as an official upstream Git submodule and is kept unchanged.

Initialize the submodule after cloning the project:

```sh
git submodule update --init
```

## Principles

- Do not modify the upstream kernel. Prefer existing extension points, overlays, public APIs, and independent services.
- Reuse mature components where appropriate and validate compatibility against pinned versions.
- Keep extensions focused; handle unrelated upstream issues separately.
- Keep documentation in English and Simplified Chinese, with extension-specific details under the corresponding extension.

## Layout

```text
extensions/        Independent extensions and their documentation
upstream/ragflow/  Official RAGFlow submodule at the pinned commit
build/             Generated build workspace (ignored by Git)
```

Build scripts are not yet implemented. Builds should apply overlays in the generated workspace, keeping the upstream submodule unchanged.
