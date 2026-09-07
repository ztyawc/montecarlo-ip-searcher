# Monte Carlo IP Searcher（mcis）

一个 **Cloudflare IP 优选**工具：用蒙特卡洛搜索算法，在更少探测次数下，从 IPv4/IPv6 网段里找到更快、更稳定的 IP。

## 为什么选择 mcis？

**传统 IP 优选工具的两难困境：**
- **扫描少了** → 随机打靶，可能错过真正的好 IP
- **扫描多了** → 耗时太长，还容易触发运营商风控

**mcis 用指定预算探索网段，并把一部分后续探测投入已有成功结果的区域。**

3000 次探测相对 180 万地址约为 1/600 的预算，这个比例不代表能保证找到全局最优。效果取决于可用 IP 的分布和失败率；本轮合成对照在集中分布中更好，在分散或高失败场景中未普遍优于随机采样。测试方法与完整结果见 [第二、三批修复说明](docs/reliability-search-fixes.md)。

**效率对比（以 Cloudflare IPv4 为例）：**

| 工具/模式 | 探测次数 | 说明 |
|-----------|----------|------|
| 全段暴力扫描 | ~180 万 | Cloudflare IPv4 约 180 万个 IP |
| CloudflareSpeedTest 默认 | ~1 万 | 每个 /24 段随机测 1 个 |
| **mcis 推荐配置** | **3000** | 智能搜索，聚焦有潜力区域 |

**mcis 如何做到的？**

| 特性 | 说明 |
|------|------|
| **递进式下钻** | 发现某个子网表现好，就继续细分探索这个区域，而不是把时间浪费在差的区域 |
| **多头分散探索** | 多个搜索头并行探索不同区域，通过"排斥力"机制避免都陷入同一个局部最优 |
| **贝叶斯优化** | 使用 Thompson Sampling 算法，自动平衡"探索新区域"和"利用已知好区域"，无需手动调参 |
| **多次测试取平均** | 每个 IP 默认测试 6 次，跳过首次握手开销，取平均值，结果更稳定准确 |

## 下载安装

[本仓库 Release](https://github.com/ztyawc/montecarlo-ip-searcher/releases/latest) 提供已发布的命令行版本，下载解压后可在终端运行。源码同步不会自动发布新版本；使用本次修复及 Android 客户端时，请按下面的源码构建说明构建。

### Android App

新版 Android 客户端位于 [`flutter-app`](flutter-app/README.md)，采用 Flutter＋`flutter_miuix` 的 HyperOS 风格界面，通过 Android 平台通道复用 Go 搜索核心，支持 IPv4/IPv6、下载测速、Colo 筛选，以及私有 SOCKS `0x80` / `0x82`。包含优选、结果、设置三个页面和浅色/深色主题。

构建目标为 `arm64-v8a`，最低支持 Android 8.0。旧版 Java 界面保留在 [`android-app`](android-app/README.md)，用于回退和共享第一阶段的进程生命周期实现。完整源码包的同步方法见 [`GITHUB_SYNC.md`](GITHUB_SYNC.md)。

## 推荐配置

**直接复制使用，无需调参：**

```bash
# IPv4 推荐配置
./mcis -v --out text --cidr-file ./ipv4cidr.txt --budget 3000 --concurrency 100

# IPv6 推荐配置
./mcis -v --out text --cidr-file ./ipv6cidr.txt --budget 4000 --heads 16 --concurrency 100
```

从源码运行：

```bash
# IPv4
go run ./cmd/mcis -v --out text --cidr-file ./ipv4cidr.txt --budget 3000 --concurrency 100

# IPv6
go run ./cmd/mcis -v --out text --cidr-file ./ipv6cidr.txt --budget 4000 --heads 16 --concurrency 100
```

**为什么 IPv6 配置不同？**
- IPv6 地址空间远大于 IPv4，需要更多探测次数（budget）才能收敛
- 更多搜索头（heads）可以并行探索更广的区域，避免陷入局部最优

**提示：** 推荐在晚高峰时段运行测试，因为此时不同 IP 之间的延迟差异更明显，算法更容易找到最优解。

## 参数速查表

### 常用参数

| 参数 | 默认值 | 推荐值 | 说明 |
|------|--------|--------|------|
| `--budget` | 2000 | IPv4: 3000, IPv6: 4000 | 总探测次数，越大结果越稳定 |
| `--concurrency` | 200 | 100 | 并发探测数 |
| `--heads` | 4 | IPv4: 4, IPv6: 16 | 搜索头数量，越多探索越广 |
| `--top` | 20 | 20 | 输出 Top N 个最优 IP |
| `--timeout` | 3s | 3s | 单次探测超时 |
| `-v` | 关闭 | 开启 | 显示搜索进度 |
| `--out` | jsonl | text | 输出格式：text/jsonl/csv |

### 高级参数（一般无需修改）

| 参数 | 默认值 | 取值范围 | 说明 |
|------|--------|----------|------|
| `--beam` | 32 | 16-64 | 每个搜索头保留的候选数 |
| `--diversity-weight` | 0.3 | 0-1 | 多样性权重，越高越分散探索 |
| `--split-interval` | 20 | 10-30 | 每 N 个样本检查一次拆分 |
| `--min-samples-split` | 5 | 3-10 | 前缀至少采样 N 次才允许拆分 |
| `--split-step-v4` | 2 | 1-8 | IPv4 下钻步长（如 /16→/18） |
| `--split-step-v6` | 4 | 1-16 | IPv6 下钻步长（如 /32→/36） |
| `--max-bits-v4` | 24 | 1-32 | IPv4 最大前缀长度 |
| `--max-bits-v6` | 56 | 1-128 | IPv6 最大前缀长度 |
| `--rounds` | 6 | 3-10 | 每个 IP 测试次数 |
| `--skip-first` | 1 | 0-3 | 跳过前 N 次测试（去除握手开销） |
| `--seed` | 0 | 有符号 64 位整数 | 随机种子（0=时间种子） |

## 参数详解

### 基础参数

**输入网段：**
- `--cidr`：直接指定 CIDR，可重复使用。例：`--cidr 1.1.1.0/24 --cidr 1.0.0.0/24`
- `--cidr-file`：从文件读取 CIDR，每行一个，支持 `#` 注释

**搜索控制：**
- `--budget`：总探测次数。**越大越稳定，但耗时越长**。IPv6 空间大，建议 4000+
- `--concurrency`：并发数。建议 50-200，过高可能导致网络拥塞
- `--top`：输出前 N 个最优 IP

**输出控制：**
- `--out`：输出格式
  - `text`：人类可读格式（推荐日常使用）
  - `jsonl`：JSON Lines 格式（适合程序解析）
  - `csv`：CSV 格式（适合导入表格）
- `--out-file`：输出到文件（默认输出到终端）
- `-v`：显示搜索进度（强烈推荐开启）

文件输出先写入同目录临时文件，编码、刷新与关闭均成功后再替换目标文件；失败不会发布半写结果。已有文件保留权限，新文件默认仅当前用户可读写。`--out-file` 接受普通文件路径，符号链接、目录和设备文件会在扫描前拒绝；需要管道时使用标准输出。

### 搜索算法参数

- `--heads`：搜索头数量。多个搜索头并行探索不同区域，通过"排斥力"机制避免都跑到同一个局部最优。IPv6 建议 8-16
- `--beam`：每个搜索头保留并评分的候选前缀数上限；持续轮换引入新候选，数值越大单次选择的计算量越大
- `--diversity-weight`：多样性权重（0-1），显式设置 `0` 可关闭该项惩罚
- `--seed`：`0` 每次运行生成时间种子；实际种子写入 `--out debug` 的 `stats.seed` 和详细日志。相同输入、种子与反馈顺序可重现采样过程，真实网络和并发响应顺序仍会影响结果

后续细分不会越过 `--max-bits-v4/v6`，单次最多生成 256 个子网；动态树容量默认最多 65,536 个节点，达到上限后继续搜索现有网段。初始输入网段超过该数时仍保留全部输入，并停止进一步扩展。

### 探测配置

- `--host`：目标域名，同时设置 TLS SNI 和 HTTP Host header。默认 `example.com`
- `--path`：请求路径。默认 `/cdn-cgi/trace`（Cloudflare 标准端点）
- `--timeout`：单次探测超时。注意：实际超时 = timeout × rounds
- `--rounds`：每个 IP 测试次数。默认 6 次，取平均值减少波动
- `--skip-first`：跳过前 N 次测试。默认 1（跳过首次握手开销）

**提示：** 使用你自己的网站作为 `--host`，可以确保优选出的 IP 对你的网站生效：

```bash
./mcis -v --out text --cidr-file ./ipv4cidr.txt --host your-domain.com --budget 3000 --concurrency 100
```

## 可选功能

### 私有 SOCKS 0x80/0x82 代理优选

默认情况下，延迟探测和下载测速都直接连接候选 IP。配置 `--private-socks` 后，两种测速都会改为通过私有 SOCKS 代理建立到候选 IP 的 TCP 隧道：

```text
mcis -> 私有 SOCKS 代理 -> 候选 IP:443
```

该协议不是标准 SOCKS5：它使用私有认证方法 `0x80` 或 `0x82`，并对客户端发出的所有字节执行 XOR `0xFF`；服务端返回数据保持原样。当前仅支持 TCP。

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--private-socks-config` | （空） | 跨平台 JSON 配置文件路径 |
| `--private-socks` | （空） | 代理地址，格式为 `host:port`；为空时保持直连 |
| `--private-socks-username` | （空） | 19 字节账号，通常从 JSON 配置读取 |
| `--private-socks-password` | （空） | 密码，通常从 JSON 配置读取 |
| `--private-socks-method` | `0x80` | 私有认证方法，可选 `0x80` 或 `0x82` |
| `--private-socks-timeout` | `10s` | 连接代理及完成私有 SOCKS 握手的最长时间 |

推荐复制仓库里的 `private-socks.example.json`，保存为 `private-socks.json` 后填写真实配置。JSON 格式在 Windows、macOS 和 Linux 上完全一致：

```json
{
  "server": "proxy.example.com",
  "port": 10800,
  "username": "1234567890123456789",
  "password": "your-password",
  "method": "0x80",
  "handshake_timeout": "10s"
}
```

需要使用 `0x82` 时，只需在配置文件中改为：

```json
"method": "0x82"
```

`0x80` 使用单字节质询和 `USERNAME + PASSWORD` 作为 HMAC-SHA256 密钥；`0x82` 使用 4 字节质询和 `USERNAME + MD5(PASSWORD)十六进制字符串` 作为密钥，并附加协议规定的 21 字节固定数据。

Linux/macOS：

```bash
cp private-socks.example.json private-socks.json
chmod 600 private-socks.json

./mcis -v --out text \
  --cidr-file ./ipv4cidr.txt \
  --private-socks-config ./private-socks.json \
  --timeout 5s \
  --budget 3000 \
  --concurrency 50
```

Windows PowerShell：

```powershell
Copy-Item .\private-socks.example.json .\private-socks.json

.\mcis.exe -v --out text `
  --cidr-file .\ipv4cidr.txt `
  --private-socks-config .\private-socks.json `
  --timeout 5s `
  --budget 3000 `
  --concurrency 50
```

也可以继续使用环境变量，或直接传入命令行参数：

```bash
export MCIS_PRIVATE_SOCKS='proxy.example.com:10800'
export MCIS_PRIVATE_SOCKS_USERNAME='1234567890123456789'
export MCIS_PRIVATE_SOCKS_PASSWORD='your-password'
```

配置优先级为：命令行参数 > JSON 配置文件 > 环境变量 > 默认值。这样可以用同一个配置文件，在临时测试时只覆盖某一个参数。

`private-socks.json` 包含明文密码，不要上传到公共仓库或发送给他人；Linux/macOS 建议设置为仅当前用户可读。模板文件只包含示例值。

代理模式测得的是“代理出口到候选 IP”的链路表现，加上本机到代理入口的固定开销，不代表本机直连候选 IP 的性能。私有 SOCKS 握手也计入首轮连接时间；如代理链路较慢，建议把 `--timeout` 调到 `5s` 或更高，并适当降低 `--concurrency`。

### CDN 节点过滤

根据 CDN 机房代码（colo）过滤结果：

- `--colo`：白名单，只保留指定机房。例：`--colo HKG,SJC`
- `--colo-exclude`：黑名单，排除指定机房。例：`--colo-exclude LAX,DFW`

两者只能二选一。

### 下载测速

对排名靠前的 IP 进行下载速度测试：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--download-top` | 5 | 对 Top N IP 测速（0=关闭） |
| `--download-bytes` | 50000000 | 下载大小（字节）；使用 `--download-url` 时不传则默认不限制 |
| `--download-timeout` | 45s | 单 IP 测速超时 |
| `--download-url` | （空） | 自定义测速文件地址（见下方说明） |
| `--download-mode` | `all` | 测速模式：`all`（测速前 N 个）或 `sequential`（顺序测速直到成功 N 个） |

**自定义测速地址：** 由于 Cloudflare 默认测速端点 `speed.cloudflare.com/__down` 对生成的下载文件大小可能存在限制，可通过 `--download-url` 指定自定义的测速文件地址。

自定义地址必须使用 HTTPS；原端口、转义路径和查询字符串会保留，无路径时请求 `/`。连接目标仍为正在测试的 IP，TLS 验证和 Host 使用原地址的主机信息。非法 URL、用户信息或 fragment 会在扫描前报错；详细日志省略查询字符串。

**指定 `--download-url` 时，默认不限制下载大小**：会下载完整文件直至 EOF，再按实际字节数与耗时计算速度。若需限制流量或时间，可加 `--download-bytes N`（最多读取 N 字节后停止）。未指定自定义 URL 时，仍使用默认 50MB 测速。

```bash
# 自定义地址：默认下载完整文件再算速度
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-url https://your-domain.com/path/to/largefile

# 自定义地址且限制只下载前 50MB
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-url https://your-domain.com/path/to/largefile --download-bytes 50000000
```

**注意：** 不限制大小时单次下载可能很大，请视情况调大 `--download-timeout`；流量约等于「文件大小 × 参与测速的 IP 数」。

**可用的测速大文件地址：** 可使用自己部署在 Cloudflare 后的静态大文件；或使用走 Cloudflare CDN 的公开下载链接（如厂商官网的安装包、镜像等）。社区整理的可选地址可参考 [CloudflareSpeedTest 讨论区](https://github.com/XIU2/CloudflareSpeedTest/discussions/490)。

**测速模式说明：**

- `all`（默认）：测速前 `--download-top` 个 IP，不管是否成功
- `sequential`：按排名顺序逐个测速，**直到成功数达到 `--download-top` 时立即停止**，可节省时间

```bash
# 默认模式：测速前 5 个 IP（可能有些会失败）
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-top 5 --download-mode all

# 顺序模式：按顺序测速，直到 5 个成功就停（如果前 5 个都成功，就只测 5 个）
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-top 5 --download-mode sequential
```

### DNS 自动上传

搜索完成后，自动将优选 IP 上传到 DNS 服务商。支持 **Cloudflare** 和 **Vercel**。程序先输出或保存扫描结果，再开始 DNS 更新；DNS 失败会返回非零退出码，已保存的结果仍然保留。

上传候选必须同时通过探测和下载测速。程序从全部成功测速结果中按下载速度排序，再应用上传数量上限，包含 `sequential` 模式中后续测速成功的 IP。没有成功候选时跳过更新。

| 参数 | 说明 |
|------|------|
| `--dns-provider` | DNS 服务商：`cloudflare` 或 `vercel` |
| `--dns-token` | API Token（或用环境变量 `CF_API_TOKEN` / `VERCEL_TOKEN`） |
| `--dns-zone` | Zone ID（Cloudflare）或域名（Vercel），或用环境变量 `CF_ZONE_ID` |
| `--dns-subdomain` | 子域名前缀（如 `cf` 会创建 `cf.example.com`） |
| `--dns-upload-count` | 上传 IP 数量（默认与 `--download-top` 相同） |
| `--dns-timeout` | 整次 DNS 更新的最长时间，默认 `60s`；从结果输出完成后开始计算，包含失败恢复 |
| `--dns-team-id` | Vercel Team ID，可选；也可使用环境变量 `VERCEL_TEAM_ID` |

更新前会完整读取记录分页，保留已有的相同 IP，先创建并确认缺少的新记录，再删除本次快照中的过期记录。仅处理本次上传涉及的地址族：只上传 IPv4 时保留 AAAA。单次 API 请求最长 `10s`，删除或最终确认失败时，在剩余期限内尝试恢复旧记录及其属性，并保留新增记录；恢复不完整会明确报错。详细行为与测试记录见 [DNS 修复说明](docs/dns-safety.md)。

示例：

```bash
# Cloudflare（使用环境变量）
export CF_API_TOKEN="your_token"
export CF_ZONE_ID="your_zone_id"
./mcis --cidr-file ./ipv4cidr.txt --dns-provider cloudflare --dns-subdomain cf -v

# Vercel
./mcis --cidr-file ./ipv4cidr.txt --dns-provider vercel --dns-zone example.com --dns-subdomain cf --dns-token YOUR_TOKEN -v
```

## 自带网段文件

仓库自带 Cloudflare 高可见度网段（从 `bgp.he.net/AS13335` 抓取，visibility > 90%）：

- `ipv4cidr.txt`：IPv4 网段
- `ipv6cidr.txt`：IPv6 网段

更新日期：2026-05-16。

## CIDR 文件格式

- 每行一个 CIDR
- 支持空行和 `#` 注释

```text
# IPv4
1.1.0.0/16
1.0.0.0/16

# IPv6
2606:4700::/32
```

## 输出格式说明

### text 格式

每行包含：rank、ip、score_ms、ok/status、prefix、colo

### jsonl 格式

一行一个 JSON，包含完整字段：ip、prefix、ok、status、connect_ms、tls_ms、ttfb_ms、total_ms、score_ms、trace 等

### csv 格式

常用字段列，适合导入表格分析。

## 常见问题

**Q: 为什么全部 `ok=false`？**

常见原因：
- 网络无法直连到目标 IP 的 443 端口
- 本地防火墙拦截
- 目标不支持当前 host/path 组合

建议：调大 `--timeout`，先用默认的 `example.com` + `/cdn-cgi/trace` 测试。

**Q: 代理环境下能用吗？**

默认模式仍然**强制直连**，忽略 `HTTP_PROXY/HTTPS_PROXY/NO_PROXY` 环境变量。只有显式设置 `--private-socks` 或 `MCIS_PRIVATE_SOCKS` 时，延迟探测和下载测速才会使用上述私有 SOCKS `0x80/0x82` 代理。

## 构建

需要 Go 1.25+：

```bash
go build -o mcis ./cmd/mcis
```

## License

GNU General Public License v3.0（GPL-3.0）
