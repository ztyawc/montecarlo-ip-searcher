import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

const defaultSettings = <String, Object>{
  'ip_version': 0,
  'host': 'www.cloudflare.com',
  'path': '/cdn-cgi/trace',
  'cidrs': '',
  'budget': '2000',
  'concurrency': '200',
  'top': '20',
  'heads': '4',
  'timeout': '3',
  'rounds': '6',
  'skip_first': '1',
  'colo_mode': 0,
  'colo': '',
  'download_enabled': false,
  'download_top': '3',
  'download_mb': '10',
  'download_timeout': '45',
  'download_url': '',
  'download_mode': 0,
  'proxy_enabled': false,
  'proxy_address': '',
  'proxy_username': '',
  'proxy_method': 0,
  'proxy_timeout': '10',
  'theme': 0,
};

abstract class ScanBridge {
  Stream<Map<String, dynamic>> get events;
  Future<Map<String, dynamic>> settings();
  Future<Map<String, dynamic>> snapshot();
  Future<void> save(Map<String, Object> settings);
  Future<void> start(Map<String, Object> settings);
  Future<void> stop();
}

class AndroidScanBridge implements ScanBridge {
  static const _control = MethodChannel('com.ztyawc.mcis/control');
  static const _events = EventChannel('com.ztyawc.mcis/events');
  @override
  Stream<Map<String, dynamic>> get events => _events
      .receiveBroadcastStream()
      .map((value) => Map<String, dynamic>.from(value as Map));
  @override
  Future<Map<String, dynamic>> settings() async => Map<String, dynamic>.from(
    await _control.invokeMethod<Map>('settings') ?? {},
  );
  @override
  Future<Map<String, dynamic>> snapshot() async => Map<String, dynamic>.from(
    await _control.invokeMethod<Map>('snapshot') ?? {},
  );
  @override
  Future<void> save(Map<String, Object> settings) =>
      _control.invokeMethod('saveSettings', settings);
  @override
  Future<void> start(Map<String, Object> settings) =>
      _control.invokeMethod('start', settings);
  @override
  Future<void> stop() => _control.invokeMethod('stop');
}

class ScanResult {
  ScanResult(Map value) : data = Map<String, dynamic>.from(value);
  final Map<String, dynamic> data;
  String get ip => data['ip'] as String? ?? '';
  String get colo => (data['trace'] as Map?)?['colo'] as String? ?? '—';
  double get latency => (data['score_ms'] as num?)?.toDouble() ?? 0;
  bool get downloadOk => data['download_ok'] == true;
  double get mbps => (data['download_mbps'] as num?)?.toDouble() ?? 0;
  String get speedLabel => downloadOk
      ? '${mbps.toStringAsFixed(2)} Mbps'
      : data['download_error'] != null
      ? '测速失败'
      : '未测速';
}

class ScanController extends ChangeNotifier {
  ScanController(this.bridge);
  final ScanBridge bridge;
  final Map<String, Object> values = Map.of(defaultSettings);
  StreamSubscription<Map<String, dynamic>>? _subscription;
  Timer? _saveTimer;
  bool ready = false, _disposed = false, _starting = false;
  String password = '', status = 'idle', error = '';
  int completed = 0, total = 0, runId = 0;
  List<ScanResult> results = [];
  List<String> logs = [];
  Map<String, dynamic> stats = {};
  bool get running =>
      _starting ||
      ['preparing', 'scanning', 'downloading', 'stopping'].contains(status);
  double get progress => total == 0 ? 0 : (completed / total).clamp(0, 1);
  String get statusLabel => switch (status) {
    'preparing' => '准备扫描',
    'scanning' => '正在优选',
    'downloading' => '正在测速',
    'stopping' => '正在停止',
    'stopped' => '已停止',
    'completed' => '优选完成',
    'error' => '扫描未完成',
    _ => '准备就绪',
  };
  String text(String key) => values[key] as String;
  int index(String key) => values[key] as int;
  bool enabled(String key) => values[key] == true;

  Future<void> initialize() async {
    try {
      final saved = await bridge.settings();
      if (_disposed) return;
      for (final key in defaultSettings.keys) {
        final value = saved[key];
        if (value != null &&
            value.runtimeType == defaultSettings[key].runtimeType) {
          values[key] = value;
        }
      }
      for (final entry in {
        'ip_version': 1,
        'colo_mode': 2,
        'download_mode': 1,
        'proxy_method': 1,
        'theme': 2,
      }.entries) {
        if (index(entry.key) < 0 || index(entry.key) > entry.value) {
          values[entry.key] = 0;
        }
      }
      // Subscribe after the initial snapshot so an old snapshot cannot replace a newer event.
      applySnapshot(await bridge.snapshot());
      if (_disposed) return;
      _subscription = bridge.events.listen(
        applySnapshot,
        onError: (Object e) => reportError('进度连接失败：${_message(e)}'),
      );
      ready = true;
      notifyListeners();
    } catch (e) {
      reportError('无法连接扫描核心：${_message(e)}');
    }
  }

  void applySnapshot(Map<String, dynamic> data) {
    if (_disposed) return;
    final incomingId = (data['runId'] as num?)?.toInt() ?? 0;
    if (incomingId < runId) return;
    runId = incomingId;
    status = data['status'] as String? ?? 'idle';
    error = data['error'] as String? ?? '';
    completed = (data['completed'] as num?)?.toInt() ?? 0;
    total = (data['total'] as num?)?.toInt() ?? 0;
    results = (data['results'] as List? ?? [])
        .whereType<Map>()
        .where((r) => r['ok'] == true)
        .map(ScanResult.new)
        .toList();
    logs = (data['logs'] as List? ?? []).whereType<String>().toList();
    stats = Map<String, dynamic>.from(data['stats'] as Map? ?? {});
    notifyListeners();
  }

  void setValue(String key, Object value) {
    if (!defaultSettings.containsKey(key)) return;
    values[key] = value;
    _saveTimer?.cancel();
    _saveTimer = Timer(const Duration(milliseconds: 350), flushSettings);
    notifyListeners();
  }

  Future<void> flushSettings() async {
    _saveTimer?.cancel();
    try {
      await bridge.save(Map.of(values));
    } catch (e) {
      reportError('参数保存失败：${_message(e)}');
    }
  }

  String? validate() {
    String? check(
      String key,
      String title,
      num min,
      num max, {
      bool integer = true,
    }) {
      final n = num.tryParse(text(key).trim());
      if (n == null ||
          !n.isFinite ||
          n < min ||
          n > max ||
          (integer && int.tryParse(text(key).trim()) == null)) {
        return '$title需为 $min–$max${integer ? ' 的整数' : ''}';
      }
      return null;
    }

    final host = text('host').trim();
    if (host.isEmpty || host.contains(RegExp(r'[/\s:]'))) {
      return '目标域名只填写主机名，例如 www.cloudflare.com';
    }
    if (!text('path').trim().startsWith('/')) return '探测路径需以 / 开头';
    final rules = <String, (String, num, num)>{
      'budget': ('扫描数量', 1, 1000000),
      'concurrency': ('并发数', 1, 2000),
      'top': ('保留结果', 1, 1000),
      'heads': ('搜索头数', 1, 64),
      'rounds': ('每 IP 轮数', 1, 100),
      'skip_first': ('跳过首轮', 0, 99),
    };
    for (final entry in rules.entries) {
      final (name, min, max) = entry.value;
      final problem = check(entry.key, name, min, max);
      if (problem != null) return problem;
    }
    if (num.parse(text('top')) > num.parse(text('budget'))) {
      return '保留结果不能大于扫描数量';
    }
    if (num.parse(text('skip_first')) >= num.parse(text('rounds'))) {
      return '跳过首轮需小于每 IP 轮数';
    }
    final timeout = check('timeout', '连接超时', .1, 300, integer: false);
    if (timeout != null) return timeout;
    if (index('colo_mode') != 0 &&
        !RegExp(
          r'^[A-Za-z]{3}(\s*,\s*[A-Za-z]{3})*$',
        ).hasMatch(text('colo').trim())) {
      return '机房代码需为三位字母，多个用英文逗号分隔';
    }
    if (enabled('download_enabled')) {
      for (final entry in {
        'download_top': ('测速 IP 数', 1, 100),
        'download_mb': ('测速流量', 1, 10000),
      }.entries) {
        final (name, min, max) = entry.value;
        final problem = check(entry.key, name, min, max);
        if (problem != null) return problem;
      }
      final problem = check(
        'download_timeout',
        '测速超时',
        1,
        3600,
        integer: false,
      );
      if (problem != null) return problem;
      final url = text('download_url').trim();
      final uri = Uri.tryParse(url);
      if (url.isNotEmpty &&
          (uri == null || uri.scheme != 'https' || uri.host.isEmpty)) {
        return '测速地址需为完整的 HTTPS 链接';
      }
    }
    if (enabled('proxy_enabled')) {
      if (text('proxy_address').trim().isEmpty) {
        return '请填写代理地址和端口';
      }
      if (utf8.encode(text('proxy_username').trim()).length != 19) {
        return '私有 SOCKS 用户名需恰好 19 字节';
      }
      if (password.isEmpty) return '请填写代理密码（仅用于本次打开期间）';
      final problem = check('proxy_timeout', '代理握手超时', .1, 300, integer: false);
      if (problem != null) return problem;
    }
    return null;
  }

  Future<void> start() async {
    if (running || !ready) return;
    final problem = validate();
    if (problem != null) {
      reportError(problem);
      return;
    }
    _starting = true;
    error = '';
    notifyListeners();
    try {
      await bridge.start({
        ...values,
        if (enabled('proxy_enabled')) 'proxy_password': password,
      });
    } catch (e) {
      reportError(_message(e));
    } finally {
      _starting = false;
      if (!_disposed) notifyListeners();
    }
  }

  Future<void> stop() async {
    if (!running || status == 'stopping') return;
    try {
      await bridge.stop();
    } catch (e) {
      reportError(_message(e));
    }
  }

  void reportError(String message) {
    if (!_disposed) {
      error = message;
      notifyListeners();
    }
  }

  static String _message(Object e) =>
      e is PlatformException ? e.message ?? e.code : e.toString();
  @override
  void dispose() {
    _disposed = true;
    _saveTimer?.cancel();
    _subscription?.cancel();
    password = '';
    super.dispose();
  }
}
