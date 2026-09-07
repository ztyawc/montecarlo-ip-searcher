# 第一阶段：结果可信度与任务停止修复

> 2026-09-06 更新：本文保留此前阶段的实现记录。其中列为未包含的 DNS 差异更新与恢复，已在后续[首批 DNS 修复](dns-safety.md)中实现；文末的 DNS 风险提示描述的是当时的代码。

> 2026-09-07 更新：自定义下载地址、参数校验、随机种子、后验公式、细分上限、Beam 候选集和连接释放也已完成后续修复，见[审计第二、三批说明](reliability-search-fixes.md)。本文末尾仍保留原阶段的范围记录。

基线：Go `7d7b4fc`；Android 基于之前保存的 `0.3.0-android.2` 源码补丁。
本次 Android 版本：`0.3.1-android.3`，versionCode `3`，包名和界面样式不变。

## 已实现

- HTTPS 探测和下载均不跟随重定向，返回 `redirect_<状态码>`，不把其他地址的结果记到候选 IP。
- 响应体读取失败、超时、取消和不完整内容不能算成功。探测响应限制为 64 KiB，超限返回 `response_too_large`。
- `--probe-mode auto` 为默认值：`/cdn-cgi/trace`（可带 query）要求合法的客户端 `ip` 和三位大写 `colo`；其他路径按普通 HTTP 健康检查处理。
- `--probe-mode trace` 强制校验 Trace；`--probe-mode http` 接受完整的普通 2xx 响应，包括健康检查中的 204。Trace 中的 IP 是客户端公网地址，不与候选服务器 IP 比较。
- 下载禁用透明解压，请求 `Accept-Encoding: identity`，按实际读取响应体字节计速。TLS 证书验证和私有 SOCKS 行为保持不变。
- `--download-min-bytes` 默认 `1000000`（1 MB）。不足样本、读取错误、声明长度不完整、默认端点提前结束时 `download_ok=false` 且速度为 0；字节数和耗时保留作诊断。
- 自定义文件可以小于配置的下载上限，但必须完整且达到最小样本；主动达到下载上限属于正常结束。这个上限是响应体读取上限，不是包含 TLS/TCP 开销的精确流量限额。
- 所有 IP（包括 `/32`、`/128`）在调度前做全局唯一预留。随机采样发生碰撞后使用游标寻找剩余地址，不再回退到重复 IP。
- 地址空间耗尽时排空已提交任务并正常结束；预算是唯一 IP 数的上限，不要求重复凑满。
- 输入网段删除重复及包含项，不合并相邻网段。复用 Engine 顺序执行时会清空上次预留。
- 失败探测保留在网段统计和汇总中，不进入可用 TopN；显式重新提交同一 IP 的结果会替换旧值，失败复查会移除旧候选，不保留历史幸运最小值。
- `--out debug` 增加 stats；详细日志增加 summary，分别记录唯一 IP、HTTP 尝试数、完成数、成功数、失败数和空间耗尽标记。HTTP 尝试数包括连接失败的尝试，不是独立 IP 数或成功 HTTP 响应数。
- Android 每次扫描拥有独立 RunSession，延迟强杀只操作原任务；停止或销毁后才启动的进程会被立即结束。异常路径统一清理子进程和流读取资源，过期任务不能更新新任务页面。
- 没有可用结果时显示“扫描完成：无可用 IP”，与程序异常区分。

## 验证

在仓库根目录运行：

```bash
MCIS_PRIVATE_SOCKS_LIVE=0 go test -race -cover ./... -timeout 60s
MCIS_PRIVATE_SOCKS_LIVE=0 go test -race -count=10 ./internal/probe ./internal/engine ./internal/cidr -timeout 60s
go vet ./...
```

网络测试仅使用本地模拟传输和本机 TLS 测试服务，不扫描公网，不修改 DNS。
新增用例覆盖合法 Trace、普通 2xx/204、错误 Trace、所有常见重定向、响应体中断与超时、下载样本和长度、IPv4/IPv6 地址耗尽、重叠 CIDR、多并发预算、Engine 复用、TopN 过滤与更新。

Android 进程管理有不依赖模拟器的 JVM 测试：

```bash
mkdir -p android-app/build/session-tests
javac -d android-app/build/session-tests \
  android-app/app/src/main/java/com/ztyawc/mcis/RunSession.java \
  android-app/tests/com/ztyawc/mcis/RunSessionTest.java
java -cp android-app/build/session-tests com.ztyawc.mcis.RunSessionTest
```

包括 7 个回归场景，以及 200 次并发启动/停止竞态检查。它们验证的是任务管理逻辑，不能替代 Android 真机生命周期测试。

## 本阶段未包含

- 前台服务、切后台继续扫描、网络切换处理、结果持久化、JSONL 进度事件协议。
- 统一入围复测、基于中位数/波动/成功率的综合排名。本阶段保留原有多轮探测和延迟排序策略。
- Beam 候选集实现、时间种子修正、后验公式修正、细分上限修正、性能基准和自适应并发。
- 自定义下载 URL 的端口与转义路径修复、DNS 差异更新与回滚。
- GitHub 推送和正式签名。正式 APK 必须沿用原签名密钥才能覆盖安装；不要为发布随意替换密钥。

虽然失败候选已从榜单移除，DNS 模块“先删后建”的风险仍然存在，本阶段不建议启用自动 DNS 更新。
