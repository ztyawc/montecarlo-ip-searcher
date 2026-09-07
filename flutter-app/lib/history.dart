import 'scan_result.dart';

class ScanHistorySummary {
  ScanHistorySummary.fromMap(Map value)
    : id = _text(value, 'id'),
      startedAt = _time(value, 'startedAt'),
      finishedAt = _time(value, 'finishedAt'),
      status = _status(value),
      ipVersion = _ipVersion(value),
      host = _host(value),
      budget = _integer(value, 'budget'),
      completed = _integer(value, 'completed'),
      total = _integer(value, 'total'),
      resultCount = _integer(value, 'resultCount', maximum: 1000);

  final String id, status, host;
  final DateTime startedAt, finishedAt;
  final int ipVersion, budget, completed, total, resultCount;

  String get statusLabel => switch (status) {
    'completed' => '已完成',
    'stopped' => '已停止',
    'interrupted' => '已中断',
    _ => '扫描失败',
  };

  static String _text(Map value, String key) {
    final field = value[key];
    if (field is! String || field.isEmpty) {
      throw const FormatException('历史记录内容不完整');
    }
    return field;
  }

  static String _host(Map value) {
    final host = value['host'];
    if (host is! String) throw const FormatException('历史记录域名无效');
    return host.isEmpty ? '未记录域名' : host;
  }

  static int _integer(Map value, String key, {int maximum = 1000000}) {
    final field = value[key];
    if (field is! int || field < 0 || field > maximum) {
      throw const FormatException('历史记录数值无效');
    }
    return field;
  }

  static DateTime _time(Map value, String key) =>
      DateTime.fromMillisecondsSinceEpoch(
        _integer(value, key, maximum: 8640000000000000),
      );

  static String _status(Map value) {
    final status = _text(value, 'status');
    if (!const [
      'completed',
      'stopped',
      'error',
      'interrupted',
    ].contains(status)) {
      throw const FormatException('历史记录状态无效');
    }
    return status;
  }

  static int _ipVersion(Map value) {
    final version = _integer(value, 'ipVersion');
    if (version != 4 && version != 6) {
      throw const FormatException('历史记录 IP 类型无效');
    }
    return version;
  }
}

class ScanHistoryEntry extends ScanHistorySummary {
  ScanHistoryEntry.fromMap(super.value)
    : results = _results(value),
      stats = Map<String, dynamic>.unmodifiable(
        Map<String, dynamic>.from(value['stats'] as Map? ?? {}),
      ),
      super.fromMap() {
    if (results.length != resultCount) {
      throw const FormatException('历史记录结果数量不一致');
    }
  }

  final List<ScanResult> results;
  final Map<String, dynamic> stats;

  static List<ScanResult> _results(Map value) {
    final rows = value['results'];
    if (rows is! List || rows.length > 1000) {
      throw const FormatException('历史记录结果无效');
    }
    return List<ScanResult>.unmodifiable(
      rows.map((row) {
        if (row is! Map ||
            row['ok'] != true ||
            row['ip'] is! String ||
            (row['ip'] as String).isEmpty) {
          throw const FormatException('历史记录 IP 结果无效');
        }
        for (final key in ['score_ms', 'download_mbps']) {
          final number = row[key];
          if (number != null && (number is! num || !number.isFinite)) {
            throw const FormatException('历史记录测速数值无效');
          }
        }
        final trace = row['trace'];
        if (trace != null &&
            (trace is! Map ||
                (trace['colo'] != null && trace['colo'] is! String))) {
          throw const FormatException('历史记录机房信息无效');
        }
        if (row['download_error'] != null && row['download_error'] is! String) {
          throw const FormatException('历史记录测速状态无效');
        }
        return ScanResult(row);
      }),
    );
  }
}
