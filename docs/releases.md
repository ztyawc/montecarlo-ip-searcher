# Release 构建与发布

发布内容为 Windows、Linux、macOS 的 amd64 / arm64 命令行压缩包，以及 Flutter Android arm64-v8a 图形界面 APK。Android 最低版本为 8.0，版本号来自 `flutter-app/pubspec.yaml`；当前源码为 `0.4.1+5`。旧 Java 客户端保留源码，不同时发布另一份同包名 APK。

## 构建环境

工作流固定使用 Flutter 3.47.2、Go 1.26.3、JDK 17、Android Platform 36、Build Tools 36.0.0 和 NDK 28.2.13676358。项目自己的 Gradle Wrapper 固定为 9.3.1，Android Gradle Plugin 为 9.1.0。无需将本机 SDK、生成的 Go 核心或缓存提交到仓库。

发布前执行 Go 测试与静态检查、Flutter 分析与测试、Java 原生逻辑检查，并核对源码清单。Windows amd64 和 Linux amd64 命令行包会检查解压后的程序能否启动；其他架构执行交叉编译及产物检查。APK 会验证包名、版本、架构、原生核心、签名和压缩包完整性。这些检查不能替代 Android 真机上的网络、生命周期和性能测试。

## Android 签名

仓库的 Actions Secrets 需要以下四项：

| 名称 | 内容 |
| --- | --- |
| `ANDROID_KEYSTORE_BASE64` | 正式 JKS / PKCS12 密钥库文件的 Base64 内容 |
| `ANDROID_KEYSTORE_PASSWORD` | 密钥库密码 |
| `ANDROID_KEY_ALIAS` | 发布密钥别名 |
| `ANDROID_KEY_PASSWORD` | 对应私钥密码 |

工作流在 APK 构建完成后才读取签名凭据，将密钥写入运行器的临时目录，执行对齐、正式签名与验证。签名凭据缺失会使发布失败，不会退回调试签名或上传未签名 APK。密钥和密码不进入源码、构建缓存或发布附件。

后续版本必须沿用相同的 Android 签名，且递增 `versionCode`，才能覆盖升级。请在 GitHub Secrets 之外保留一份由仓库所有者控制的密钥和密码备份；GitHub Secrets 不能用于取回原始密钥。

## 发布步骤

1. 将经过检查的代码合并到 `main`，确定版本。Android 的发布标签应与 `pubspec.yaml` 对齐，例如 `v0.4.0` 对应 `0.4.0+4`。
2. 将该版本标签指向要发布的提交。在 Actions 中打开 Release 工作流，手动运行并填写标签；默认不立即公开发布。
3. 等待全部检查和构建成功。工作流将各平台附件、Android 签名验证信息与 `SHA256SUMS` 汇总上传到同一份 Release 草稿。
4. 下载核验附件，确认各平台齐全，再公开草稿。需要直接自动公开时，可在手动运行时启用发布选项。

工作流也保留 `release.created` 入口，供从 GitHub Release 页面创建版本后自动补齐附件。这个入口中的 Release 可能在构建前已公开；需要完整附件准备好才公开时，请使用手动草稿流程。普通推送与 PR 合并不会自动创建 Release。

## 失败与重跑

任何必需检查、构建或签名失败，均不会进入产物汇总发布步骤。修复构建问题后，可以针对同一草稿重跑；已经公开的版本不覆盖已有同名附件。需要改变公开版本的程序内容时，应发布一个新版本，并递增 Android 版本号。

压缩包包含程序、许可证、使用说明、IPv4 / IPv6 网段和私有 SOCKS 配置示例。`SHA256SUMS` 用于验证下载内容；Android 签名证书指纹用于确认后续版本使用同一发布身份。
