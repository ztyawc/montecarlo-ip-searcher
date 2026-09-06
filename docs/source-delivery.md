# MCIS 0.4.0 源码交付记录

日期：2026-09-06。

后续变更：2026-09-07 的审计第二、三批修复、测试与构建结果见 [本批修复说明](reliability-search-fixes.md)。以下保留原交付时的记录；根目录 `SOURCE_FILES.sha256` 随后续源码更新。

## 范围

- Go 基线：`7d7b4fcb5a14529be8e36499d17bbbecdd56035e`，叠加已完成的第一阶段修复。
- Flutter＋flutter_miuix Android APP：`0.4.0+4`，`com.ztyawc.mcis`，arm64。
- 交付时工作目录缺失的 Flutter 文件按当轮改动记录恢复；页面、参数、桥接、进程管理及测试一并保留。
- 补齐可提交的 Gradle Wrapper、依赖锁文件、项目构建和 GitHub 同步说明。
- 后台扫描、结果持久化等后续功能未包含在该次交付中；该次工作未推送远程仓库。

## 本次实际验证

1. Flutter 3.47.2 / Dart 3.13.2，`flutter pub get` 成功。
2. `flutter analyze --no-pub`：No issues found。
3. `flutter test --no-pub`：8 项测试全部通过，包含页面切换、启动停止、键盘、大字体和长 IPv6。
4. `MCIS_PRIVATE_SOCKS_LIVE=0 go test ./... -timeout 60s`：全部通过。
5. 使用 Android API 35 和 Flutter release 的 `flutter.jar` 对 NativeScanner、RunSession 和原生测试进行 JVM 编译。
6. `NativeConfigTest`：8 项参数和命令行测试通过。
7. `RunSessionTest`：7 项回归场景通过，包含 200 次并发竞态检查。

本次源码整理未重新执行完整 Gradle APK 打包，也未进行 Android 真机测试。源码恢复与构建工具文件整理不保证复现先前 APK 的逐字节哈希。

## 导出

ZIP 内 `montecarlo-ip-searcher/` 是项目根目录，包含完整 Go、Flutter、Android 工程、网段、许可证和文档。
导出保留 `.gitignore`、`.github`、Gradle Wrapper 和 `pubspec.lock`；排除 Git 历史、本地 SDK 路径、构建缓存、APK、签名密钥及运行时私有配置。
`SOURCE_FILES.sha256` 记录源码包内各文件的 SHA-256，便于同步前后核对。
