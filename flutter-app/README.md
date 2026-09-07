# MCIS Flutter Android 0.4.0

Flutter＋flutter_miuix 界面，通过 Android 平台通道调用 Go 扫描核心。

- 版本：`0.4.0+4`，包名：`com.ztyawc.mcis`。
- Android 8.0 及以上，仅 `arm64-v8a`。
- 页面：优选、结果、设置、运行日志、IP 详情。
- 浅色/深色/跟随系统主题；IPv4/IPv6、Colo 筛选、下载测速、私有 SOCKS 0x80/0x82。
- 保留第一阶段的 HTTP 校验、下载有效性、IP 去重及 RunSession 修复。第二阶段尚未实施。

2026-09-07 审计第二、三批修复已接入：诊断统计先从原文解析，再对展示日志脱敏；异常数字不会中断主线程，关闭代理时不会把残留密码传入新任务。Go 核心同步修复下载地址、参数校验及搜索算法，见 [本批修复说明](../docs/reliability-search-fixes.md)。这里的审计批次与此前规划的后台扫描等功能阶段分别记录。

正式安装包从 [本仓库 Release](https://github.com/ztyawc/montecarlo-ip-searcher/releases/latest) 下载，选择 `android-arm64-v8a.apk`。发布工作流会在构建后单独正式签名并验证，未签名的本机构建产物不会作为正式 APK 上传。后续更新沿用同一签名；不同签名的历史测试版不能直接覆盖安装。维护者配置见 [发布说明](../docs/releases.md)。

## 构建

使用 Flutter **3.47.2**（Dart **3.13.2**）、Go **1.25.5 或更高版本**、完整 JDK **17**、Android SDK Platform **36**、Build Tools **36.0.0** 和 NDK **28.2.13676358**。目标 SDK 保持 35。

2026-09-06 已在本机升级并验证 Flutter 3.47.2 / Dart 3.13.2；`flutter_miuix` 1.1.1 的最低要求是 Dart 3.12.2。依赖解析、静态分析和 8 项 Flutter 测试通过，环境版本与备份位置见 [开发环境记录](../docs/development-environment.md)。

必须从完整项目构建：`flutter-app` 会引用根目录的 Go 源码、网段文件，以及 `android-app` 中的 RunSession 和资源。

```bash
cd flutter-app
flutter pub get
flutter analyze
flutter test
flutter build apk --release --target-platform android-arm64
```

Go 需位于 PATH。Gradle 原生任务也支持 `-PgoExecutable=/absolute/path/to/go`。
构建会自动生成 Go `libmcis.so`、复制 IPv4/IPv6 网段和 RunSession，无需手动放入编译后的核心文件。
初次构建需要联网下载 Flutter、Dart 和 Android/Gradle 依赖。

不创建 `android/key.properties` 时，release APK 保持**未签名**。Gradle 输出：

```text
build/app/outputs/apk/release/app-release-unsigned.apk
```

Flutter 也会将 APK 复制到 `build/app/outputs/flutter-apk/app-release.apk`；该文件名不代表已经签名。
不要删除 `extractNativeLibs=true`、`useLegacyPackaging=true` 或将 Go 核心改为普通 assets：核心需要在原生库目录中作为可执行文件启动。

需要自行配置签名时，在本地创建已被 gitignore 排除的 `android/key.properties`：

```properties
storeFile=/absolute/path/to/your-release.jks
storePassword=YOUR_STORE_PASSWORD
keyAlias=YOUR_KEY_ALIAS
keyPassword=YOUR_KEY_PASSWORD
```

## 源码结构

| 路径 | 职责 |
| --- | --- |
| `lib/main.dart` | miuix 页面与交互 |
| `lib/scan_controller.dart` | 参数校验、状态、MethodChannel/EventChannel |
| `android/app/src/main/java/com/ztyawc/mcis/FlutterMainActivity.java` | Flutter 宿主和通道注册 |
| `android/app/src/main/java/com/ztyawc/mcis/NativeScanner.java` | Go 进程、JSONL 结果、进度与日志 |
| `../android-app/app/src/main/java/com/ztyawc/mcis/RunSession.java` | 共用的进程生命周期控制 |
| `../cmd/mcis/`、`../internal/` | Go CLI 与扫描核心 |
| `test/`、`android/tests/` | Flutter 与原生参数回归测试 |

## 当前行为

设置沿用旧版 `mcis_settings`。密码只保留于内存；运行时写入应用私有缓存的临时文件，运行结束删除，重启清理异常退出遗留文件。密码不进入命令行、设置或复制的日志。应用备份关闭。

每个扫描会话只拥有一个进程；停止与迟到启动发生竞争时销毁迟到进程，旧会话不能更新新会话状态。原生层将密集结果事件合并到约 80 ms 一次，日志保留最近 200 条。

请保持应用在前台。页面切换不停止扫描，退出 Activity 会停止扫描；结果尚未持久化。最终结果在扫描及测速结束后显示。失败下载显示“测速失败”，不会按成功速度展示。

miuix 1.1.1 导航项高度固定，导航标签缩放限制为 1.2；正文与输入框继续跟随系统字体大小，大字或窄屏时双列输入切为单列。

## 测试

```bash
flutter analyze
flutter test
```

包括参数继承、密码不持久化、无效参数拒绝启动、重复启动保护、停止重启、旧事件隔离、失败测速显示、页面切换、键盘遮挡及长 IPv6/大字体布局。

可选截图：提供本地字体，使用 `--update-goldens` 生成 `test/review/`：

```bash
MCIS_REVIEW_FONT=/path/to/NotoSansSC.ttf \
MCIS_REVIEW_ICONS=/path/to/flutter/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf \
flutter test --update-goldens
```

这些字体只用于测试，不随 APP 打包。测试渲染器截图不能代替 Android 真机验证。

项目保留根目录 GPL-3.0 许可证。Flutter 使用 BSD-3-Clause；flutter_miuix 使用 Apache-2.0。
