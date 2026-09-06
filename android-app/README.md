# MCIS Android

当前源码版本：`0.3.1-android.3`。已包含 [第一阶段修复](../docs/phase1-reliability.md)和 [2026-09-07 审计第二、三批修复](../docs/reliability-search-fixes.md)。

MCIS 的原生 Android 客户端，使用 iOS grouped settings 风格界面，并直接运行仓库中的 Go 搜索核心。

## 功能

- IPv4 / IPv6 蒙特卡洛 IP 优选
- 实时进度；完整保存结果，每页显示 50 条，“复制全部”包含完整排名
- 可选下载测速与 Colo 节点筛选
- 私有 SOCKS `0x80` / `0x82` 认证
- 自定义 CIDR、域名和算法参数
- 仅申请 `INTERNET` 权限，不自动修改 DNS

## 构建要求

- JDK 17
- Android SDK Platform 35；AGP 8.7.3 默认使用 Build Tools 34.0.0
- Go 1.25.5 或兼容的新版 Go 工具链

Gradle 会在 `preBuild` 阶段把 `../cmd/mcis` 交叉编译为 Android arm64 PIE 核心，并把仓库根目录的 IPv4/IPv6 CIDR 列表复制到应用资源目录。因此 Android 工程不重复保存这些生成文件。

```bash
cd android-app
./gradlew assembleDebug
```

如果 `go` 不在 `PATH` 中，可明确指定可执行文件：

```bash
./gradlew -PgoExecutable=/absolute/path/to/go assembleDebug
```

生成的调试 APK 位于：

```text
app/build/outputs/apk/debug/app-debug.apk
```

当前只打包 `arm64-v8a`，最低支持 Android 8.0（API 26）。应用将 Go PIE 放在原生库目录并作为子进程运行，所以清单中的 `extractNativeLibs=true` 是有意保留的。

## 正式签名

构建目录及本工程的 `.signing/` 目录被 `.gitignore` 排除。签名密钥应保存在 `.signing/` 中，密码保留在本地；它们和 APK 不属于源码同步清单。当前 release 未配置签名，发布时请使用自己的密钥对 APK 执行 `zipalign` 和 `apksigner`。
