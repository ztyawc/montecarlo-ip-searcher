import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_miuix/miuix.dart';

import 'scan_controller.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(McisApp(controller: ScanController(AndroidScanBridge())));
}

class McisApp extends StatefulWidget {
  const McisApp({super.key, required this.controller});
  final ScanController controller;
  @override
  State<McisApp> createState() => _McisAppState();
}

class _McisAppState extends State<McisApp> with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    if (!widget.controller.ready) widget.controller.initialize();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.inactive) widget.controller.flushSettings();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    widget.controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: widget.controller,
    builder: (context, _) => MaterialApp(
      title: 'MCIS',
      debugShowCheckedModeBanner: false,
      themeMode: ThemeMode.values[widget.controller.index('theme')],
      theme: _materialTheme(Brightness.light),
      darkTheme: _materialTheme(Brightness.dark),
      builder: (context, child) => MiuixTheme(
        data: MiuixThemeData.of(Theme.of(context).brightness),
        child: child!,
      ),
      home: Material(child: ScanShell(controller: widget.controller)),
    ),
  );
  ThemeData _materialTheme(Brightness brightness) => ThemeData(
    brightness: brightness,
    useMaterial3: true,
    colorScheme: ColorScheme.fromSeed(
      seedColor: const Color(0xff3482ff),
      brightness: brightness,
    ),
    scaffoldBackgroundColor: brightness == Brightness.light
        ? const Color(0xfff5f5f5)
        : const Color(0xff101010),
    splashFactory: NoSplash.splashFactory,
  );
}

class ScanShell extends StatefulWidget {
  const ScanShell({super.key, required this.controller});
  final ScanController controller;
  @override
  State<ScanShell> createState() => _ScanShellState();
}

class _ScanShellState extends State<ScanShell> {
  int page = 0;
  bool showLogs = false;
  ScanResult? detail;
  final snacks = MiuixSnackbarHostState();
  ScanController get scan => widget.controller;
  static const titles = ['优选', '结果', '设置'];

  void navigate(int index) {
    FocusManager.instance.primaryFocus?.unfocus();
    setState(() {
      page = index;
      showLogs = false;
      detail = null;
    });
  }

  Future<void> copy(String value) async {
    await Clipboard.setData(ClipboardData(text: value));
    if (mounted) snacks.showSnackbar('已复制');
  }

  @override
  void dispose() {
    snacks.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final colors = MiuixTheme.of(context).colors;
    final dark = Theme.of(context).brightness == Brightness.dark;
    return AnnotatedRegion<SystemUiOverlayStyle>(
      value: (dark ? SystemUiOverlayStyle.light : SystemUiOverlayStyle.dark)
          .copyWith(
            statusBarColor: Colors.transparent,
            systemNavigationBarColor: Colors.transparent,
          ),
      child: PopScope(
        canPop: detail == null && !showLogs && page == 0,
        onPopInvokedWithResult: (didPop, result) {
          if (didPop) return;
          setState(() {
            if (detail != null) {
              detail = null;
            } else if (showLogs) {
              showLogs = false;
            } else {
              page = 0;
            }
          });
        },
        child: MiuixScaffold(
          topBar: MiuixSmallTopAppBar(
            title: showLogs ? '运行日志' : 'MCIS',
            navigationIcon: showLogs
                ? IconButton(
                    onPressed: () => setState(() => showLogs = false),
                    icon: const Icon(Icons.arrow_back_rounded),
                    tooltip: '返回',
                  )
                : null,
            actions: [
              if (!showLogs)
                IconButton(
                  onPressed: () => setState(() => showLogs = true),
                  icon: const Icon(Icons.receipt_long_rounded, size: 23),
                  tooltip: '运行日志',
                ),
            ],
          ),
          bottomBar: MediaQuery.viewInsetsOf(context).bottom > 0
              ? null
              : MediaQuery.withClampedTextScaling(
                  // miuix 1.1.1 navigation items have a fixed 64 px height. Body text remains fully scalable.
                  maxScaleFactor: 1.2,
                  child: MiuixNavigationBar(
                    children: List.generate(
                      3,
                      (index) => MiuixNavigationBarItem(
                        selected: page == index,
                        onPressed: () => navigate(index),
                        label: titles[index],
                        icon: Icon(
                          [
                            Icons.radar_rounded,
                            Icons.format_list_bulleted_rounded,
                            Icons.tune_rounded,
                          ][index],
                        ),
                      ),
                    ),
                  ),
                ),
          snackbarHost: MiuixSnackbarHost(state: snacks),
          content: (padding) => Padding(
            padding: EdgeInsets.only(
              bottom: MediaQuery.viewInsetsOf(context).bottom,
            ),
            child: Stack(
              children: [
                if (showLogs) _logs(padding) else _page(padding),
                MiuixOverlayBottomSheet(
                  show: detail != null,
                  title: 'IP 详情',
                  onDismissRequest: () => setState(() => detail = null),
                  content: detail == null
                      ? const SizedBox.shrink()
                      : _details(detail!, colors.onSurface),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _page(EdgeInsets padding) {
    final children = switch (page) {
      0 => _home(),
      1 => _results(),
      _ => _settings(),
    };
    return ListView(
      key: PageStorageKey('page-$page'),
      keyboardDismissBehavior: ScrollViewKeyboardDismissBehavior.onDrag,
      padding: padding + const EdgeInsets.fromLTRB(20, 10, 20, 28),
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
          child: Text(
            titles[page],
            style: const TextStyle(fontSize: 32, fontWeight: FontWeight.w700),
          ),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(8, 0, 8, 20),
          child: Text(
            ['找到更快的连接', '本次扫描的可用 IP', '让每次优选更合适'][page],
            style: TextStyle(
              fontSize: 13,
              color: MiuixTheme.of(context).colors.onSurfaceVariantSummary,
            ),
          ),
        ),
        ...children,
      ],
    );
  }

  List<Widget> _home() {
    final c = MiuixTheme.of(context).colors;
    return [
      if (!scan.ready)
        const Padding(padding: EdgeInsets.all(12), child: Text('正在连接扫描核心…')),
      MiuixCard(
        cornerRadius: 28,
        insideMargin: const EdgeInsets.all(24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: ShapeDecoration(
                    color: c.primary.withValues(alpha: .10),
                    shape: const MiuixSquircleBorder(cornerRadius: 17),
                  ),
                  child: Icon(
                    scan.running ? Icons.radar_rounded : Icons.public_rounded,
                    color: c.primary,
                    size: 26,
                  ),
                ),
                const Spacer(),
                _tag(
                  scan.index('ip_version') == 0 ? 'IPv4' : 'IPv6',
                  c.primary,
                ),
              ],
            ),
            const SizedBox(height: 24),
            Text(
              scan.statusLabel,
              style: const TextStyle(
                fontSize: 28,
                fontWeight: FontWeight.w700,
                letterSpacing: -.5,
              ),
            ),
            const SizedBox(height: 8),
            Text(
              _statusDescription(),
              style: TextStyle(
                fontSize: 14,
                color: c.onSurfaceVariantSummary,
                height: 1.5,
              ),
            ),
            const SizedBox(height: 24),
            if (scan.running || scan.status != 'idle') ...[
              Semantics(
                label: '扫描进度',
                value: '${(scan.progress * 100).round()}%',
                child: MiuixLinearProgressIndicator(
                  height: 7,
                  progress:
                      [
                        'preparing',
                        'downloading',
                        'stopping',
                      ].contains(scan.status)
                      ? null
                      : scan.progress,
                ),
              ),
              const SizedBox(height: 20),
            ],
            Row(
              children: [
                Expanded(
                  child: _metric(
                    '扫描数量',
                    scan.running || scan.status != 'idle'
                        ? '${scan.completed}'
                        : scan.text('budget'),
                  ),
                ),
                Expanded(
                  child: _metric(
                    scan.status == 'idle' ? '并发数' : '可用结果',
                    scan.status == 'idle'
                        ? scan.text('concurrency')
                        : '${scan.results.length}',
                  ),
                ),
                Expanded(
                  child: _metric(
                    '测速',
                    scan.enabled('download_enabled') ? '已开启' : '未开启',
                  ),
                ),
              ],
            ),
            const SizedBox(height: 24),
            SizedBox(
              width: double.infinity,
              child: MiuixButton(
                key: const ValueKey('scan-action'),
                minHeight: 54,
                cornerRadius: 18,
                enabled: scan.ready && scan.status != 'stopping',
                colors: MiuixButtonDefaults.buttonColorsPrimary(context),
                onPressed: () async {
                  FocusManager.instance.primaryFocus?.unfocus();
                  if (scan.running) {
                    await scan.stop();
                  } else {
                    await scan.start();
                  }
                },
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Icon(
                      scan.running
                          ? Icons.stop_rounded
                          : Icons.play_arrow_rounded,
                      size: 23,
                      color: c.onPrimary,
                    ),
                    const SizedBox(width: 8),
                    Text(
                      scan.running ? '停止扫描' : '开始优选',
                      style: const TextStyle(
                        fontSize: 17,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
      if (scan.error.isNotEmpty) _errorCard(),
      _section('扫描目标'),
      MiuixCard(
        cornerRadius: 24,
        insideMargin: const EdgeInsets.all(16),
        child: Column(
          children: [
            AbsorbPointer(
              absorbing: scan.running,
              child: MiuixTabRowWithContour(
                colors: MiuixTabRowColors(
                  backgroundColor: c.secondaryContainer,
                  contentColor: c.onSurfaceVariantSummary,
                  selectedBackgroundColor: c.surfaceContainer,
                  selectedContentColor: c.primary,
                ),
                tabs: const ['IPv4', 'IPv6'],
                selectedTabIndex: scan.index('ip_version'),
                onTabSelected: (value) => scan.setValue('ip_version', value),
              ),
            ),
            const SizedBox(height: 16),
            _field('host', '目标域名', icon: Icons.language_rounded),
            const SizedBox(height: 12),
            Row(
              children: [
                Icon(
                  Icons.route_rounded,
                  size: 16,
                  color: c.onSurfaceVariantSummary,
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    scan.text('cidrs').trim().isEmpty
                        ? '使用内置 Cloudflare 网段'
                        : '使用自定义网段',
                    style: TextStyle(
                      fontSize: 13,
                      color: c.onSurfaceVariantSummary,
                    ),
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
      _section('增强选项'),
      MiuixCard(
        cornerRadius: 24,
        child: Column(
          children: [
            _toggle(
              'download_enabled',
              '下载测速',
              '对优选 IP 进行实际下载测速',
              Icons.speed_rounded,
            ),
            const MiuixHorizontalDivider(),
            _toggle(
              'proxy_enabled',
              '私有 SOCKS',
              '支持 0x80 / 0x82 认证',
              Icons.shield_outlined,
            ),
          ],
        ),
      ),
      const SizedBox(height: 14),
      MiuixCard(
        cornerRadius: 24,
        onPressed: () => navigate(2),
        insideMargin: const EdgeInsets.all(20),
        child: Row(
          children: [
            Icon(Icons.tune_rounded, color: c.primary, size: 22),
            const SizedBox(width: 12),
            const Expanded(
              child: Text('调整扫描参数', style: TextStyle(fontSize: 16)),
            ),
            Icon(Icons.chevron_right_rounded, color: c.onSurfaceVariantSummary),
          ],
        ),
      ),
      const SizedBox(height: 18),
      Text(
        '扫描期间请保持应用在前台。结果保留至下次扫描或关闭应用。',
        style: TextStyle(
          color: c.onSurfaceVariantSummary,
          fontSize: 12,
          height: 1.5,
        ),
        textAlign: TextAlign.center,
      ),
    ];
  }

  String _statusDescription() => switch (scan.status) {
    'scanning' => '已探测 ${scan.completed} / ${scan.total} 个 IP',
    'downloading' => '正在验证实际下载速度，请稍候',
    'completed' =>
      scan.stats['exhausted'] == true
          ? '网段已扫描完毕 · ${scan.results.length} 个优选结果'
          : '已找到 ${scan.results.length} 个优选结果',
    'stopped' => '本次扫描已停止，可调整参数后重新开始',
    'stopping' => '正在结束本次扫描任务',
    'error' => '查看下方提示或运行日志',
    _ => '从多个网段中，优选稳定、低延迟的 IP',
  };

  List<Widget> _results() {
    final c = MiuixTheme.of(context).colors;
    return [
      if (scan.error.isNotEmpty) _errorCard(),
      if (scan.running) ...[
        MiuixCard(
          cornerRadius: 24,
          insideMargin: const EdgeInsets.all(20),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                scan.statusLabel,
                style: const TextStyle(
                  fontSize: 17,
                  fontWeight: FontWeight.w600,
                ),
              ),
              const SizedBox(height: 12),
              MiuixLinearProgressIndicator(
                progress: scan.status == 'downloading' ? null : scan.progress,
              ),
              const SizedBox(height: 12),
              const Text('扫描及测速结束后显示最终结果', style: TextStyle(fontSize: 13)),
            ],
          ),
        ),
        const SizedBox(height: 14),
      ],
      if (scan.results.isEmpty)
        MiuixCard(
          cornerRadius: 26,
          insideMargin: const EdgeInsets.symmetric(
            vertical: 54,
            horizontal: 24,
          ),
          child: Column(
            children: [
              Icon(
                Icons.travel_explore_rounded,
                size: 58,
                color: c.primary.withValues(alpha: .65),
              ),
              const SizedBox(height: 24),
              Text(
                scan.status == 'completed' ? '暂无可用 IP' : '等待一次新的发现',
                style: const TextStyle(
                  fontSize: 22,
                  fontWeight: FontWeight.w600,
                ),
              ),
              const SizedBox(height: 12),
              Text(
                scan.status == 'completed'
                    ? '检查网络、目标域名和网段后重试'
                    : '开始优选后，在这里查看延迟、机房和下载速度',
                style: TextStyle(color: c.onSurfaceVariantSummary, height: 1.6),
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 24),
              MiuixButton(
                onPressed: () => navigate(0),
                child: const Text('前往优选'),
              ),
            ],
          ),
        )
      else ...[
        Row(
          children: [
            Expanded(
              child: Text(
                '${scan.results.length} 个优选结果',
                style: const TextStyle(
                  fontSize: 15,
                  fontWeight: FontWeight.w600,
                ),
              ),
            ),
            MiuixButton(
              onPressed: () => copy(scan.results.map((r) => r.ip).join('\n')),
              child: const Text('复制全部'),
            ),
          ],
        ),
        const SizedBox(height: 10),
        Text(
          '按综合延迟排序 · 点击卡片查看详情',
          style: TextStyle(fontSize: 12, color: c.onSurfaceVariantSummary),
        ),
        const SizedBox(height: 16),
        ...scan.results.indexed.map(
          (entry) => Padding(
            padding: const EdgeInsets.only(bottom: 12),
            child: _resultCard(entry.$2, entry.$1 + 1),
          ),
        ),
      ],
      if (scan.stats.isNotEmpty) ...[
        _section('本次统计'),
        MiuixCard(
          cornerRadius: 24,
          insideMargin: const EdgeInsets.all(20),
          child: Wrap(
            spacing: 24,
            runSpacing: 18,
            children: [
              _metric('成功探测', '${scan.stats['successful'] ?? 0}'),
              _metric('失败探测', '${scan.stats['failed'] ?? 0}'),
              _metric('请求次数', '${scan.stats['request_attempts'] ?? 0}'),
            ],
          ),
        ),
      ],
    ];
  }

  Widget _resultCard(ScanResult result, int rank) {
    final c = MiuixTheme.of(context).colors;
    return MiuixCard(
      cornerRadius: 24,
      onPressed: () => setState(() => detail = result),
      insideMargin: const EdgeInsets.fromLTRB(20, 14, 12, 20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              _tag(
                '#${rank.toString().padLeft(2, '0')}',
                rank == 1 ? c.primary : c.onSurfaceVariantSummary,
              ),
              const SizedBox(width: 8),
              _tag(result.colo, c.onSurfaceVariantSummary),
              const Spacer(),
              IconButton(
                onPressed: () => copy(result.ip),
                icon: const Icon(Icons.copy_rounded, size: 20),
                tooltip: '复制 ${result.ip}',
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            result.ip,
            style: const TextStyle(
              fontSize: 19,
              fontWeight: FontWeight.w600,
              fontFamily: 'monospace',
            ),
          ),
          const SizedBox(height: 18),
          Wrap(
            spacing: 28,
            runSpacing: 12,
            children: [
              _metric('综合延迟', '${result.latency.toStringAsFixed(1)} ms'),
              _metric('下载速度', result.speedLabel),
            ],
          ),
        ],
      ),
    );
  }

  Widget _details(ScanResult result, Color color) => SingleChildScrollView(
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SelectableText(
          result.ip,
          style: const TextStyle(fontSize: 23, fontWeight: FontWeight.w600),
        ),
        const SizedBox(height: 20),
        for (final entry in {
          '网段': result.data['prefix'],
          '机房': result.colo,
          '综合延迟': '${result.latency.toStringAsFixed(1)} ms',
          'TCP 连接': '${result.data['connect_ms']} ms',
          'TLS 握手': '${result.data['tls_ms']} ms',
          '首字节时间': '${result.data['ttfb_ms']} ms',
          '下载速度': result.speedLabel,
          if (result.data['download_error'] != null)
            '测速错误': result.data['download_error'],
        }.entries)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 8),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SizedBox(
                  width: 100,
                  child: Text(
                    entry.key,
                    style: TextStyle(color: color.withValues(alpha: .6)),
                  ),
                ),
                Expanded(child: SelectableText('${entry.value ?? '—'}')),
              ],
            ),
          ),
        const SizedBox(height: 22),
        MiuixButton(
          colors: MiuixButtonDefaults.buttonColorsPrimary(context),
          onPressed: () => copy(result.ip),
          child: const Text('复制 IP'),
        ),
      ],
    ),
  );

  List<Widget> _settings() => [
    if (scan.running)
      const Padding(
        padding: EdgeInsets.only(bottom: 14),
        child: Text('扫描进行中，结束后可修改参数。'),
      ),
    _section('外观', first: true),
    MiuixCard(
      cornerRadius: 24,
      child: _choice('theme', '主题', const [
        '跟随系统',
        '浅色',
        '深色',
      ], alwaysEnabled: true),
    ),
    _section('基础设置'),
    _formGroup([
      _field('path', '探测路径'),
      _pair(
        _field('budget', '扫描数量', numeric: true),
        _field('concurrency', '并发数', numeric: true),
      ),
      _pair(
        _field('top', '保留结果', numeric: true),
        _field('heads', '搜索头数', numeric: true),
      ),
      _field('timeout', '连接超时 / 秒', numeric: true),
    ]),
    _section('探测轮次'),
    _formGroup([
      _pair(
        _field('rounds', '每 IP 轮数', numeric: true),
        _field('skip_first', '跳过首轮', numeric: true),
      ),
    ]),
    _section('网段与机房'),
    _formGroup([
      _field('cidrs', '自定义 CIDR · 留空使用内置网段', lines: 4),
      _choice('colo_mode', '机房筛选', const ['不筛选', '只保留', '排除']),
      if (scan.index('colo_mode') != 0) _field('colo', '机房代码 · 如 HKG,NRT,SJC'),
    ]),
    _section('下载测速'),
    MiuixCard(
      cornerRadius: 24,
      child: Column(
        children: [
          _toggle(
            'download_enabled',
            '下载测速',
            '测速会消耗移动数据或 Wi-Fi 流量',
            Icons.speed_rounded,
          ),
          if (scan.enabled('download_enabled'))
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
              child: _spaced([
                _pair(
                  _field('download_top', '测速 IP 数', numeric: true),
                  _field('download_mb', '每 IP 流量 / MB', numeric: true),
                ),
                _field('download_timeout', '测速超时 / 秒', numeric: true),
                _choice('download_mode', '测速方式', const ['全部候选', '顺序优选']),
                _field('download_url', '自定义 HTTPS 地址 · 可选'),
              ]),
            ),
        ],
      ),
    ),
    _section('私有 SOCKS'),
    MiuixCard(
      cornerRadius: 24,
      child: Column(
        children: [
          _toggle(
            'proxy_enabled',
            '启用代理',
            '仅应用于 MCIS 的扫描与测速',
            Icons.shield_outlined,
          ),
          if (scan.enabled('proxy_enabled'))
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
              child: _spaced([
                _field('proxy_address', '代理地址 · host:port'),
                _field('proxy_username', '用户名 · 19 字节'),
                _field('proxy_password', '密码 · 关闭应用后清除', password: true),
                _choice('proxy_method', '认证方式', const ['0x80', '0x82']),
                _field('proxy_timeout', '握手超时 / 秒', numeric: true),
              ]),
            ),
        ],
      ),
    ),
    const SizedBox(height: 26),
    const Text(
      'MCIS 0.4.0\nFlutter · flutter_miuix',
      textAlign: TextAlign.center,
      style: TextStyle(fontSize: 12, height: 1.8, color: Colors.grey),
    ),
    const SizedBox(height: 12),
  ];

  Widget _logs(EdgeInsets padding) => ListView(
    padding: padding + const EdgeInsets.all(20),
    children: [
      if (scan.logs.isEmpty)
        const Padding(
          padding: EdgeInsets.symmetric(vertical: 60),
          child: Center(child: Text('开始扫描后，运行信息会显示在这里')),
        )
      else ...[
        MiuixButton(
          onPressed: () => copy(scan.logs.join('\n')),
          child: const Text('复制日志'),
        ),
        const SizedBox(height: 18),
        SelectableText(
          scan.logs.join('\n\n'),
          style: const TextStyle(
            fontFamily: 'monospace',
            fontSize: 12,
            height: 1.6,
          ),
        ),
      ],
    ],
  );
  Widget _errorCard() => Padding(
    padding: const EdgeInsets.only(top: 14),
    child: MiuixCard(
      cornerRadius: 20,
      insideMargin: const EdgeInsets.all(18),
      colors: MiuixCardColors(
        color: Theme.of(context).colorScheme.errorContainer,
        contentColor: Theme.of(context).colorScheme.onErrorContainer,
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Icon(Icons.info_outline_rounded, size: 20),
          const SizedBox(width: 10),
          Expanded(
            child: Text(scan.error, style: const TextStyle(height: 1.5)),
          ),
        ],
      ),
    ),
  );
  Widget _section(String title, {bool first = false}) => Padding(
    padding: EdgeInsets.fromLTRB(8, first ? 2 : 26, 8, 12),
    child: Text(
      title,
      style: TextStyle(
        fontSize: 13,
        fontWeight: FontWeight.w500,
        color: MiuixTheme.of(context).colors.onSurfaceVariantSummary,
      ),
    ),
  );
  Widget _tag(String label, Color color) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
    decoration: BoxDecoration(
      color: color.withValues(alpha: .09),
      borderRadius: BorderRadius.circular(8),
    ),
    child: Text(
      label,
      style: TextStyle(color: color, fontSize: 12, fontWeight: FontWeight.w600),
    ),
  );
  Widget _metric(String title, String value) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(
        title,
        style: TextStyle(
          fontSize: 12,
          color: MiuixTheme.of(context).colors.onSurfaceVariantSummary,
        ),
      ),
      const SizedBox(height: 6),
      Text(
        value,
        style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w600),
      ),
    ],
  );
  Widget _toggle(String key, String title, String summary, IconData icon) =>
      MiuixSwitchPreference(
        title: title,
        summary: summary,
        value: scan.enabled(key),
        enabled: !scan.running,
        onChanged: (value) => scan.setValue(key, value),
        startAction: Padding(
          padding: const EdgeInsets.only(right: 12),
          child: Icon(icon, color: MiuixTheme.of(context).colors.primary),
        ),
      );
  Widget _choice(
    String key,
    String title,
    List<String> items, {
    bool alwaysEnabled = false,
  }) => MiuixOverlaySpinnerPreference(
    title: title,
    items: items.map((text) => MiuixDropdownItem(text: text)).toList(),
    selectedIndex: scan.index(key),
    enabled: alwaysEnabled || !scan.running,
    onSelectedIndexChange: (value) => scan.setValue(key, value),
  );
  Widget _formGroup(List<Widget> children) => MiuixCard(
    cornerRadius: 24,
    insideMargin: const EdgeInsets.all(16),
    child: _spaced(children),
  );
  Widget _spaced(List<Widget> children) => Column(
    children: [
      for (var i = 0; i < children.length; i++) ...[
        if (i > 0) const SizedBox(height: 12),
        children[i],
      ],
    ],
  );
  Widget _pair(Widget left, Widget right) => LayoutBuilder(
    builder: (context, constraints) =>
        MediaQuery.textScalerOf(context).scale(16) > 22 ||
            constraints.maxWidth < 280
        ? _spaced([left, right])
        : Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(child: left),
              const SizedBox(width: 12),
              Expanded(child: right),
            ],
          ),
  );
  Widget _field(
    String key,
    String label, {
    bool numeric = false,
    bool password = false,
    int lines = 1,
    IconData? icon,
  }) => SettingField(
    key: ValueKey(key),
    value: password ? scan.password : scan.text(key),
    label: label,
    enabled: !scan.running && scan.ready,
    numeric: numeric,
    password: password,
    lines: lines,
    icon: icon,
    onChanged: (value) {
      if (password) {
        scan.password = value;
      } else {
        scan.setValue(key, value);
      }
    },
  );
}

class SettingField extends StatefulWidget {
  const SettingField({
    super.key,
    required this.value,
    required this.label,
    required this.onChanged,
    this.enabled = true,
    this.numeric = false,
    this.password = false,
    this.lines = 1,
    this.icon,
  });
  final String value, label;
  final ValueChanged<String> onChanged;
  final bool enabled, numeric, password;
  final int lines;
  final IconData? icon;
  @override
  State<SettingField> createState() => _SettingFieldState();
}

class _SettingFieldState extends State<SettingField> {
  late final TextEditingController controller = TextEditingController(
    text: widget.value,
  );
  @override
  void didUpdateWidget(SettingField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (controller.text != widget.value) {
      controller.value = TextEditingValue(
        text: widget.value,
        selection: TextSelection.collapsed(offset: widget.value.length),
      );
    }
  }

  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => MiuixTextField(
    controller: controller,
    label: widget.label,
    onChanged: widget.onChanged,
    enabled: widget.enabled,
    singleLine: widget.lines == 1,
    minLines: widget.lines,
    maxLines: widget.lines,
    obscureText: widget.password,
    leadingIcon: widget.icon == null ? null : Icon(widget.icon, size: 21),
    keyboardType: widget.numeric
        ? const TextInputType.numberWithOptions(decimal: true)
        : widget.lines > 1
        ? TextInputType.multiline
        : TextInputType.text,
    textInputAction: widget.lines > 1
        ? TextInputAction.newline
        : TextInputAction.done,
    onSubmitted: (_) => FocusManager.instance.primaryFocus?.unfocus(),
  );
}
