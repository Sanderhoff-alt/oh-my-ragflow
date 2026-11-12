# oh-my-ragflow

[English](README.md) | 简体中文

一个独立的 RAGFlow 扩展项目，在保持上游内核不变的前提下，为 RAGFlow 增加能力。

## 当前状态

项目处于设计阶段。各扩展的范围与实现状态参见对应的 README。

## 适配基线

- RAGFlow：[`v1.0.0`](https://github.com/infiniflow/ragflow/releases/tag/v1.0.0)，提交 `db1cd5929a7885a3074547791f5ecc4d2f891739`。
- 此基线以官方上游 Git submodule 的形式记录，并保持原样。

克隆本项目后，初始化 submodule：

```sh
git submodule update --init
```

## 原则

- 不修改上游内核。优先使用已有扩展点、覆盖文件、公开 API 和独立服务。
- 适当复用成熟组件，并针对固定版本验证兼容性。
- 保持扩展范围聚焦，无关的上游问题单独处理。
- 文档保持英文和简体中文双语；扩展专属内容放在对应扩展目录中。

## 目录结构

```text
extensions/        独立扩展及其文档
upstream/ragflow/  指向固定提交的官方 RAGFlow submodule
build/             构建时生成的工作区（Git 忽略）
```

构建脚本尚未实现。构建时应在生成的工作区应用覆盖文件，保持上游 submodule 不变。
