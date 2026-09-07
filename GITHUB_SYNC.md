# 源码同步与构建

本项目包含 MCIS Go 命令行工具、Flutter＋flutter_miuix Android 客户端和旧 Java Android 客户端，以及已完成的可靠性修复、审计三批修复和配套测试。后台扫描、结果持久化等后续功能尚未实现。

源码中的下列目录需要保持同级：

- `flutter-app/`：Flutter Android 客户端，版本 `0.4.0+4`。
- `android-app/`：Java Android 客户端，版本 `0.3.1-android.3`；同时提供两端共用的 RunSession 和图标资源。
- `cmd/`、`internal/`：Go 命令行与搜索核心。
- `go.mod`、`go.sum`、`ipv4cidr.txt`、`ipv6cidr.txt`、`LICENSE`、`readme.md`。

## 同步范围

`SOURCE_FILES.sha256` 列出源码、测试、文档和构建配置；同步时另包含清单本身。APK、生成的 Go 核心、SDK、构建缓存、本机配置、真实凭据、签名密钥、备份及嵌套旧模块不在同步范围内。

从源码快照同步时，在目标仓库的克隆中按清单逐项复制，保留目标仓库的 Git 历史。若远程已有较新修改，先比较并合并；核对内容后再暂存清单内文件和清单本身。

```bash
git clone https://github.com/ztyawc/montecarlo-ip-searcher.git
cd montecarlo-ip-searcher
sha256sum -c SOURCE_FILES.sha256
git status --short
git diff --stat
```

校验清单随文件内容更新。提交前还应检查暂存差异，确保没有加入清单之外的本机文件。首批源码已通过 PR #1 合并到 `ztyawc/montecarlo-ip-searcher` 的 `main`；后续构建从 `main` 或明确的发布标签检出完整仓库。

## 自动构建范围

[Release 工作流](.github/workflows/release.yml) 支持创建 Release 事件，以及在 Actions 页面手动选择发布标签运行：

| 目标 | 当前构建方式 |
| --- | --- |
| Windows、Linux、macOS 命令行工具 | 自动构建 amd64 和 arm64，共六种组合 |
| Flutter Android APP | 自动构建 arm64-v8a APK，并用仓库 Actions Secrets 中的正式密钥签名和验证 |
| Java Android APP | 保留源码供手动构建，本次不重复发布同包名 APP |
| iOS、Windows/macOS/Linux 图形界面 APP | 尚未实现对应平台宿主和构建流程 |

普通推送和合并请求不会直接发布新版本。发布工作流包含 Go、Flutter、Java 检查、Windows/Linux 命令行启动检查和 APK 签名验证；只有所需任务全部成功，才汇总上传产物。创建新版本建议使用手动入口，先保留为草稿，核对附件后公开。已公开 Release 的同名附件不会被工作流覆盖。

签名 Secrets、发布步骤和重跑规则见 [发布说明](docs/releases.md)。Android 本机构建要求与命令见 [Flutter 说明](flutter-app/README.md)和 [Java 客户端说明](android-app/README.md)。

## 验证记录

- [首批 DNS 修复](docs/dns-safety.md)
- [审计第二、三批修复及实测](docs/reliability-search-fixes.md)
- [开发环境验证](docs/development-environment.md)
- [2026-09-06 源码交付历史记录](docs/source-delivery.md)
