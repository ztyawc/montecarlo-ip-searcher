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
