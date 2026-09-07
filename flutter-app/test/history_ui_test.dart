import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_miuix/miuix.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mcis/main.dart';
import 'package:mcis/scan_controller.dart';

import 'scan_test.dart' show FakeBridge;

class HistoryUiBridge extends FakeBridge {
  final Map<String, Map<String, dynamic>> records = {};
  final List<String> deleted = [];
  bool failHistory = false;
  bool failDelete = false;
  Completer<Map<String, dynamic>>? pendingHistory;
  Completer<Map<String, dynamic>>? pendingEntry;

  @override
  Future<Map<String, dynamic>> history() async {
    if (pendingHistory != null) return pendingHistory!.future;
    if (failHistory) throw StateError('历史读取暂时失败');
    return {'entries': records.values.toList(), 'limit': 50};
  }

  @override
  Future<Map<String, dynamic>> historyEntry(String id) async {
    if (pendingEntry != null) return pendingEntry!.future;
    return Map.of(records[id]!);
  }

  @override
  Future<void> deleteHistory(String id) async {
    if (failDelete) throw StateError('历史删除暂时失败');
    deleted.add(id);
    records.remove(id);
  }
}

Map<String, dynamic> record(
  String id, {
  String host = 'previous.example.com',
  String status = 'completed',
  int ipVersion = 4,
  List<String> ips = const ['192.0.2.18'],
}) => {
  'id': id,
  'startedAt': DateTime(2026, 9, 8, 10, 30).millisecondsSinceEpoch,
  'finishedAt': DateTime(2026, 9, 8, 10, 32).millisecondsSinceEpoch,
  'status': status,
  'host': host,
  'ipVersion': ipVersion,
  'budget': 200,
  'completed': status == 'completed' ? 200 : 120,
  'total': 200,
  'resultCount': ips.length,
  'results': [
    for (final ip in ips)
      {
        'ip': ip,
        'ok': true,
        'score_ms': 12.3,
        'trace': {'colo': 'HKG'},
      },
  ],
  'stats': {'successful': 30, 'failed': 170, 'request_attempts': 230},
};

Future<void> pumpUi(WidgetTester tester) async {
  await tester.pump();
  for (var frame = 0; frame < 16; frame++) {
    await tester.pump(const Duration(milliseconds: 100));
  }
}

Future<ScanController> mount(
  WidgetTester tester,
  HistoryUiBridge bridge, {
  Size size = const Size(390, 844),
  double textScale = 1,
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  tester.platformDispatcher.textScaleFactorTestValue = textScale;
  addTearDown(() async {
    tester.view.resetPhysicalSize();
    tester.view.resetDevicePixelRatio();
    tester.platformDispatcher.clearTextScaleFactorTestValue();
    await bridge.bus.close();
  });
  final scan = ScanController(bridge);
  await scan.initialize();
  await tester.pumpWidget(McisApp(controller: scan));
  await pumpUi(tester);
  return scan;
}

Future<void> openResults(WidgetTester tester) async {
  await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '结果'));
  await pumpUi(tester);
}

Future<void> reveal(WidgetTester tester, Finder target) async {
  await tester.scrollUntilVisible(
    target,
    180,
    scrollable: find.byType(Scrollable).first,
  );
  await Scrollable.ensureVisible(tester.element(target), alignment: .4);
  await pumpUi(tester);
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('saved results restore without replacing current scan settings', (
    tester,
  ) async {
    final bridge = HistoryUiBridge()
      ..saved = {'host': 'current.example.com', 'budget': '300'};
    bridge.records['latest'] = record('latest');
    final scan = await mount(tester, bridge);
    expect(scan.status, 'idle');
    expect(scan.results, isEmpty);
    expect(scan.selectedHistory?.id, 'latest');
    await openResults(tester);
    expect(find.byKey(const ValueKey('history-context')), findsOneWidget);
    expect(find.text('previous.example.com'), findsOneWidget);
    expect(find.text('已完成'), findsOneWidget);
    expect(scan.text('host'), 'current.example.com');
    expect(scan.text('budget'), '300');
    await reveal(tester, find.byKey(const ValueKey('show-current-results')));
    await tester.tap(find.byKey(const ValueKey('show-current-results')));
    await pumpUi(tester);
    expect(scan.selectedHistory, isNull);
    expect(find.text('本次扫描的可用 IP'), findsOneWidget);
    expect(find.byKey(const ValueKey('history-context')), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('browsing and copying history keeps a live scan independent', (
    tester,
  ) async {
    const historicalIp = '192.0.2.18';
    const currentIp = '198.51.100.90';
    final bridge = HistoryUiBridge();
    bridge.records['older'] = record(
      'older',
      ips: [historicalIp, '192.0.2.19'],
    );
    final scan = await mount(tester, bridge);
    await scan.start();
    bridge.bus.add({
      ...bridge.state,
      'completed': 100,
      'results': [
        {'ip': currentIp, 'ok': true, 'score_ms': 8},
      ],
    });
    await openResults(tester);
    await tester.tap(find.byKey(const ValueKey('open-history')));
    await pumpUi(tester);
    await tester.tap(find.byKey(const ValueKey('history-older')));
    await pumpUi(tester);
    expect(scan.running, isTrue);
    expect(scan.completed, 100);
    expect(scan.results.single.ip, currentIp);
    expect(scan.selectedHistory?.id, 'older');
    expect(find.text('扫描及测速结束后显示最终结果'), findsNothing);

    String? copied;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied = (call.arguments as Map)['text'] as String;
        }
        return null;
      },
    );
    addTearDown(() {
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        null,
      );
    });
    await reveal(tester, find.byKey(const ValueKey('copy-all-results')));
    await tester.tap(find.byKey(const ValueKey('copy-all-results')));
    await pumpUi(tester);
    expect(copied, '$historicalIp\n192.0.2.19');
    await reveal(tester, find.text(historicalIp));
    await tester.tap(find.text(historicalIp));
    await pumpUi(tester);
    expect(find.text('IP 详情'), findsOneWidget);
    expect(find.text('复制 IP'), findsOneWidget);
    await tester.binding.handlePopRoute();
    await pumpUi(tester);
    expect(find.text('IP 详情'), findsNothing);
    await tester.binding.handlePopRoute();
    await pumpUi(tester);
    expect(find.text('最近扫描'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('history-show-current')));
    await pumpUi(tester);
    expect(scan.selectedHistory, isNull);
    expect(scan.running, isTrue);
    expect(scan.results.single.ip, currentIp);
    expect(bridge.stops, 0);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'deleting history requires confirmation and preserves live data',
    (tester) async {
      final bridge = HistoryUiBridge();
      bridge.records['older'] = record('older');
      final scan = await mount(tester, bridge);
      await scan.start();
      await openResults(tester);
      await tester.tap(find.byKey(const ValueKey('open-history')));
      await pumpUi(tester);
      await tester.tap(find.byKey(const ValueKey('delete-history-older')));
      await pumpUi(tester);
      expect(find.text('删除这条记录？'), findsOneWidget);
      expect(bridge.deleted, isEmpty);
      await tester.tap(find.byKey(const ValueKey('cancel-delete-history')));
      await pumpUi(tester);
      expect(bridge.records.containsKey('older'), isTrue);
      await tester.tap(find.byKey(const ValueKey('delete-history-older')));
      await pumpUi(tester);
      await tester.tap(find.byKey(const ValueKey('confirm-delete-history')));
      await pumpUi(tester);
      expect(bridge.deleted, ['older']);
      expect(scan.history, isEmpty);
      expect(find.text('暂无历史记录'), findsOneWidget);
      expect(scan.running, isTrue);
      expect(bridge.stops, 0);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('history read and delete failures remain visible and retryable', (
    tester,
  ) async {
    final bridge = HistoryUiBridge()..failHistory = true;
    bridge.records['older'] = record('older');
    final scan = await mount(tester, bridge);
    await openResults(tester);
    await tester.tap(find.byKey(const ValueKey('open-history')));
    await pumpUi(tester);
    expect(find.byKey(const ValueKey('history-error')), findsOneWidget);
    expect(find.text('暂无历史记录'), findsNothing);
    bridge.failHistory = false;
    await tester.tap(find.byKey(const ValueKey('retry-history')));
    await pumpUi(tester);
    expect(scan.history.single.id, 'older');
    expect(find.byKey(const ValueKey('history-error')), findsNothing);
    bridge.failDelete = true;
    await tester.tap(find.byKey(const ValueKey('delete-history-older')));
    await pumpUi(tester);
    await tester.tap(find.byKey(const ValueKey('confirm-delete-history')));
    await pumpUi(tester);
    expect(scan.history.single.id, 'older');
    expect(find.byKey(const ValueKey('history-error')), findsOneWidget);
    expect(find.text('已删除记录'), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('history loading can be left without a late view replacement', (
    tester,
  ) async {
    final bridge = HistoryUiBridge();
    bridge.records['older'] = record('older');
    final scan = await mount(tester, bridge);
    scan.showCurrentResults();
    await openResults(tester);
    bridge.pendingHistory = Completer<Map<String, dynamic>>();
    await tester.tap(find.byKey(const ValueKey('open-history')));
    await pumpUi(tester);
    expect(find.text('正在读取历史记录…'), findsOneWidget);
    bridge.pendingHistory!.complete({
      'entries': bridge.records.values.toList(),
      'limit': 50,
    });
    await pumpUi(tester);
    bridge.pendingEntry = Completer<Map<String, dynamic>>();
    await tester.tap(find.byKey(const ValueKey('history-older')));
    await pumpUi(tester);
    expect(find.text('正在读取扫描记录…'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('history-show-current')));
    await pumpUi(tester);
    bridge.pendingEntry!.complete(bridge.records['older']!);
    await pumpUi(tester);
    expect(scan.selectedHistory, isNull);
    expect(find.text('本次扫描的可用 IP'), findsOneWidget);
    expect(find.byKey(const ValueKey('history-context')), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('history rows, details and confirmation fit narrow large text', (
    tester,
  ) async {
    final bridge = HistoryUiBridge();
    bridge.records['older'] = record(
      'older',
      host: 'a-very-long-target-name.for-historical-results.example.com',
      status: 'interrupted',
      ipVersion: 6,
      ips: ['2001:db8:ffff:ffff:ffff:ffff:ffff:ffff'],
    );
    await mount(tester, bridge, size: const Size(320, 740), textScale: 1.8);
    await openResults(tester);
    await tester.tap(find.byKey(const ValueKey('open-history')));
    await pumpUi(tester);
    await reveal(tester, find.byKey(const ValueKey('history-older')));
    expect(find.text('IPv6'), findsOneWidget);
    expect(find.text('已中断'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.tap(find.byKey(const ValueKey('history-older')));
    await pumpUi(tester);
    await reveal(tester, find.byKey(const ValueKey('delete-history-older')));
    await tester.tap(find.byKey(const ValueKey('delete-history-older')));
    await pumpUi(tester);
    expect(find.text('删除这条记录？'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.tap(find.byKey(const ValueKey('cancel-delete-history')));
    await pumpUi(tester);
    await reveal(tester, find.text('2001:db8:ffff:ffff:ffff:ffff:ffff:ffff'));
    await tester.tap(find.text('2001:db8:ffff:ffff:ffff:ffff:ffff:ffff'));
    await pumpUi(tester);
    expect(find.text('IP 详情'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });
}
