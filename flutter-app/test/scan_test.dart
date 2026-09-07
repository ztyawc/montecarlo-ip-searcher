import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_miuix/miuix.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mcis/main.dart';
import 'package:mcis/scan_controller.dart';

class FakeBridge implements ScanBridge {
  final bus = StreamController<Map<String, dynamic>>.broadcast(sync: true);
  Map<String, Object> saved = {};
  Map<String, Object>? started;
  Map<String, dynamic> state = {'runId': 0, 'status': 'idle'};
  int starts = 0, stops = 0;
  @override
  Stream<Map<String, dynamic>> get events => bus.stream;
  @override
  Future<Map<String, dynamic>> settings() async => Map.of(saved);
  @override
  Future<Map<String, dynamic>> snapshot() async => state;
  @override
  Future<Map<String, dynamic>> history() async => {'entries': [], 'limit': 50};
  @override
  Future<Map<String, dynamic>> historyEntry(String id) async =>
      throw StateError('History entry not found');
  @override
  Future<void> deleteHistory(String id) async {}
  @override
  Future<void> save(Map<String, Object> settings) async {
    saved = settings;
  }

  @override
  Future<void> start(Map<String, Object> settings) async {
    starts++;
    started = settings;
    state = {
      'runId': starts,
      'status': 'scanning',
      'completed': 0,
      'total': 2000,
    };
    bus.add(state);
  }

  @override
  Future<void> stop() async {
    stops++;
    bus.add({...state, 'status': 'stopped'});
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(() async {
    final font = Platform.environment['MCIS_REVIEW_FONT'];
    if (font != null) {
      final data = ByteData.sublistView(await File(font).readAsBytes());
      for (final name in ['Roboto', 'Ahem', 'monospace']) {
        await (FontLoader(name)..addFont(Future.value(data))).load();
      }
      final icons = Platform.environment['MCIS_REVIEW_ICONS'];
      if (icons != null) {
        await (FontLoader('MaterialIcons')..addFont(
              Future.value(
                ByteData.sublistView(await File(icons).readAsBytes()),
              ),
            ))
            .load();
      }
    }
  });

  test(
    'settings migrate and credentials never enter persistent settings',
    () async {
      final bridge = FakeBridge()
        ..saved = {
          'budget': '100',
          'proxy_username': '1234567890123456789',
          'theme': 99,
        };
      final scan = ScanController(bridge);
      await scan.initialize();
      expect(scan.text('budget'), '100');
      expect(scan.index('theme'), 0);
      scan.password = 'test-only-password';
      await scan.flushSettings();
      expect(bridge.saved.containsKey('proxy_password'), false);
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'invalid parameters do not launch a process; stop permits a fresh run',
    () async {
      final bridge = FakeBridge();
      final scan = ScanController(bridge);
      await scan.initialize();
      scan.values['rounds'] = '1';
      await scan.start();
      expect(bridge.starts, 0);
      expect(scan.error, contains('跳过'));
      scan.values['skip_first'] = '0';
      await scan.start();
      await scan.start();
      expect(bridge.starts, 1);
      expect(scan.running, true);
      await scan.stop();
      expect(bridge.stops, 1);
      expect(scan.running, false);
      await scan.start();
      expect(bridge.starts, 2);
      scan.applySnapshot({'runId': 1, 'status': 'completed'});
      expect(scan.status, 'scanning');
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test('disabled proxy never sends its retained in-memory password', () async {
    final bridge = FakeBridge();
    final scan = ScanController(bridge);
    await scan.initialize();
    scan.password = '2000';
    scan.values['proxy_enabled'] = true;
    scan.values['proxy_address'] = '127.0.0.1:1080';
    scan.values['proxy_username'] = '1234567890123456789';
    await scan.start();
    expect(bridge.started?['proxy_password'], '2000');
    await scan.stop();
    scan.values['proxy_enabled'] = false;
    await scan.start();
    expect(bridge.starts, 2);
    expect(bridge.started!.containsKey('proxy_password'), false);
    expect(scan.password, '2000');
    scan.dispose();
    await bridge.bus.close();
  });

  test('short or failed downloads cannot display a speed as valid', () {
    final failed = ScanResult({
      'ip': '192.0.2.1',
      'download_ok': false,
      'download_mbps': 999,
      'download_error': 'short_download',
    });
    expect(failed.speedLabel, '测速失败');
    final scan = ScanController(FakeBridge());
    scan.applySnapshot({
      'runId': 1,
      'status': 'completed',
      'results': [
        {'ip': '192.0.2.1', 'ok': false},
        {'ip': '192.0.2.2', 'ok': true},
      ],
    });
    expect(scan.results.single.ip, '192.0.2.2');
    scan.dispose();
  });

  Future<ScanController> mount(
    WidgetTester tester, {
    Brightness brightness = Brightness.light,
    Size size = const Size(390, 844),
  }) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    tester.platformDispatcher.platformBrightnessTestValue = brightness;
    addTearDown(() {
      tester.view.resetPhysicalSize();
      tester.view.resetDevicePixelRatio();
      tester.platformDispatcher.clearPlatformBrightnessTestValue();
    });
    final scan = ScanController(FakeBridge());
    await scan.initialize();
    await tester.pumpWidget(McisApp(controller: scan));
    await tester.pumpAndSettle();
    return scan;
  }

  testWidgets(
    'home and settings keep values across navigation and keyboard changes',
    (tester) async {
      final scan = await mount(tester);
      expect(find.text('准备就绪'), findsOneWidget);
      await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '设置'));
      await tester.pumpAndSettle();
      final budget = find.descendant(
        of: find.byKey(const ValueKey('budget')),
        matching: find.byType(EditableText),
      );
      await tester.ensureVisible(budget);
      await tester.enterText(budget, '123');
      await tester.pump();
      expect(scan.text('budget'), '123');
      tester.view.viewInsets = const FakeViewPadding(bottom: 300);
      addTearDown(tester.view.resetViewInsets);
      await tester.pumpAndSettle();
      await tester.ensureVisible(budget);
      await tester.pumpAndSettle();
      expect(find.byType(MiuixNavigationBar), findsNothing);
      expect(tester.getRect(budget).bottom, lessThanOrEqualTo(544));
      FocusManager.instance.primaryFocus?.unfocus();
      tester.view.resetViewInsets();
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '优选'));
      await tester.pumpAndSettle();
      expect(find.text('123'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('start and stop remain connected when switching tabs', (
    tester,
  ) async {
    final scan = await mount(tester);
    await tester.tap(find.byKey(const ValueKey('scan-action')));
    await tester.pump();
    expect(scan.running, true);
    await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '结果'));
    await tester.pump(const Duration(milliseconds: 500));
    expect(find.text('扫描及测速结束后显示最终结果'), findsOneWidget);
    await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '优选'));
    await tester.pump(const Duration(milliseconds: 500));
    await tester.tap(find.byKey(const ValueKey('scan-action')));
    await tester.pumpAndSettle();
    expect(scan.status, 'stopped');
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('long IPv6 results, details and large text do not overflow', (
    tester,
  ) async {
    final scan = await mount(tester, size: const Size(320, 740));
    tester.platformDispatcher.textScaleFactorTestValue = 1.4;
    addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
    scan.applySnapshot({
      'runId': 1,
      'status': 'completed',
      'completed': 20,
      'total': 20,
      'results': [
        {
          'ip': '2001:db8:ffff:ffff:ffff:ffff:ffff:ffff',
          'ok': true,
          'score_ms': 123.45,
          'trace': {'colo': 'HKG'},
          'download_ok': false,
          'download_error': 'short_download',
        },
      ],
    });
    await tester.tap(find.widgetWithText(MiuixNavigationBarItem, '结果'));
    await tester.pumpAndSettle();
    expect(find.text('测速失败'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.tap(find.text('2001:db8:ffff:ffff:ffff:ffff:ffff:ffff'));
    await tester.pumpAndSettle();
    expect(find.text('IP 详情'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  for (final brightness in Brightness.values) {
    testWidgets('review ${brightness.name} home', (tester) async {
      await mount(tester, brightness: brightness);
      expect(tester.takeException(), isNull);
      if (Platform.environment['MCIS_REVIEW_FONT'] != null) {
        await expectLater(
          find.byType(McisApp),
          matchesGoldenFile('review/home-${brightness.name}.png'),
        );
      }
      await tester.pumpWidget(const SizedBox());
    });
  }
}
