**开发环境验证记录（2026-09-06）**

本次为 MCIS 首批 DNS 修复升级开发环境，使用官方 Flutter stable 发行版及 pub.dev 的正式依赖。

| 组件 | 验证版本 | 说明 |
| --- | --- | --- |
| Flutter | 3.47.2 stable | 使用官方 stable 发行版，验证机的默认入口已切换到此版本 |
| Dart | 3.13.2 | 随 Flutter SDK 安装 |
| flutter_miuix | 1.1.1 | pub.dev 当前稳定版；项目依赖及锁文件保持此版本 |
| Go | 1.26.3 | 满足项目 `go 1.25.5` 要求 |
| OpenJDK | 17.0.19 | 满足 Android Gradle Plugin 9.1.0 要求 |
| Gradle（Flutter 客户端） | 9.3.1 | 由 `flutter-app/android` 的 Gradle Wrapper 安装使用，Android Gradle Plugin 9.1.0 |
| Gradle（旧 Java 客户端） | 8.9 | 2026-09-07 由 `android-app` 的 Gradle Wrapper 安装验证，Android Gradle Plugin 8.7.3 |
| Android SDK / Build Tools（Flutter） | 36 / 36.0.0 | 目标 SDK 保持 35 |
| Android SDK / Build Tools（旧 Java） | 35 / 34.0.0 | AGP 8.7.3 默认使用 Build Tools 34.0.0；2026-09-07 构建时已安装 |
| Android NDK | 28.2.13676358 | 与项目配置相同 |

Flutter stable 与 `3.47.2` 标签均指向 `d3b14c876900e553bc736ca19295fc09e3853e8e`，内置 engine revision 为 `a804b261645ef8c13eb3d5c44a5c2fb0340c5539`。安装后实际运行已确认 Flutter 和 Dart 版本；Android、Linux 测试及字体构建缓存已下载。

flutter_miuix 1.1.1 的实际约束是 Dart `^3.12.2`。原 SDK 的 Dart 3.12.0 不满足要求；本次通过升级 SDK 解决，没有降低包约束或使用依赖覆盖。发布包的 SHA-256 与项目现有锁文件一致，`flutter pub get` 成功，锁文件未变化。

环境验证已通过：Flutter 静态分析零问题、Flutter 8 项测试、NativeConfig 8 项用例、RunSession 7 个场景及 200 次并发竞态检查。`flutter doctor` 的 Android toolchain 检查通过，Gradle 工程配置检查通过。Android arm64 release APK 已构建成功，记录见 [DNS 修复说明](dns-safety.md)。

2026-09-07，审计第二、三批修复后的 Flutter 和旧 Java 客户端 release APK 均构建成功。Flutter 测试增加至 9 项，Java 原生测试共 31 个场景及 200 次并发检查，当前结果见 [本批修复说明](reliability-search-fixes.md)。旧 Java 构建为适配本机内存，单次将 Gradle 最大堆设为 1 GiB，没有修改项目配置。

原 Flutter 3.44.0 SDK、修改前的项目文件与环境信息备份保留在验证机，不随源码同步。本地 SDK 绝对路径存于各 Android 工程被忽略的 `local.properties`。两个工程的 `gradlew` 已补齐可执行权限。

官方来源：[Flutter 3.47.2](https://github.com/flutter/flutter/tree/3.47.2)、[Flutter SDK archive](https://docs.flutter.dev/install/archive)、[flutter_miuix 包](https://pub.dev/packages/flutter_miuix)、[flutter_miuix 源码](https://github.com/ChuxinNeko/flutter_miuix)。
