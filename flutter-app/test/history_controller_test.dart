import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:mcis/scan_controller.dart';

import 'scan_test.dart' show FakeBridge;

Map<String, dynamic> record(String id, {String ip = '192.0.2.1'}) => {
  'id': id,
  'startedAt': 1700000000000,
  'finishedAt': 1700000005000,
  'status': 'completed',
  'ipVersion': 4,
  'host': 'www.cloudflare.com',
  'budget': 20,
  'completed': 20,
  'total': 20,
  'resultCount': 1,
  'results': [
    {'ip': ip, 'ok': true, 'score_ms': 25.0, 'download_ok': false},
  ],
  'stats': {'unique_ips': 20, 'successful': 1},
};

class HistoryBridge extends FakeBridge {
  List<Map<String, dynamic>> records = [];
  Future<Map<String, dynamic>> Function()? read;
  Future<Map<String, dynamic>> Function(String)? readEntry;
  bool failRead = false, failDelete = false;
  int historyReads = 0;

  Map<String, dynamic> get listing => {
    'entries': [
      for (final entry in records)
        Map<String, dynamic>.of(entry)
          ..remove('results')
          ..remove('stats'),
    ],
    'limit': 50,
  };

  @override
  Future<Map<String, dynamic>> history() async {
    historyReads++;
    if (failRead) throw StateError('history unavailable');
    return read != null ? read!() : listing;
  }

  @override
  Future<Map<String, dynamic>> historyEntry(String id) async =>
      readEntry != null
      ? readEntry!(id)
      : Map.of(records.singleWhere((entry) => entry['id'] == id));

  @override
  Future<void> deleteHistory(String id) async {
    if (failDelete) throw StateError('delete failed');
    records.removeWhere((entry) => entry['id'] == id);
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test(
    'reopening selects the latest saved result without changing settings',
    () async {
      final bridge = HistoryBridge()
        ..records = [record('latest'), record('older', ip: '192.0.2.2')]
        ..saved = {'host': 'current.example', 'budget': '100'};
      final scan = ScanController(bridge);
      await scan.initialize();
      expect(scan.ready, isTrue);
      expect(scan.selectedHistory?.id, 'latest');
      expect(scan.displayedResults.single.ip, '192.0.2.1');
      expect(scan.results, isEmpty);
      expect(scan.status, 'idle');
      expect(scan.text('host'), 'current.example');
      expect(scan.text('budget'), '100');
      await scan.start();
      expect(scan.running, isTrue);
      expect(scan.selectedHistory, isNull);
      expect(scan.history.map((entry) => entry.id), ['latest', 'older']);
      expect(bridge.records, hasLength(2));
      scan.dispose();

      bridge.state = {'runId': 0, 'status': 'idle'};
      final reopened = ScanController(bridge);
      await reopened.initialize();
      expect(reopened.selectedHistory?.id, 'latest');
      reopened.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'browsing history does not replace the live task or its results',
    () async {
      final bridge = HistoryBridge()..records = [record('saved')];
      final scan = ScanController(bridge);
      await scan.initialize();
      await scan.start();
      await scan.selectHistory('saved');
      scan.applySnapshot({
        'runId': 1,
        'status': 'completed',
        'completed': 2000,
        'total': 2000,
        'results': [
          {'ip': '198.51.100.1', 'ok': true, 'score_ms': 10},
        ],
      });
      expect(scan.selectedHistory?.id, 'saved');
      expect(scan.displayedResults.single.ip, '192.0.2.1');
      expect(scan.results.single.ip, '198.51.100.1');
      expect(scan.completed, 2000);
      scan.showCurrentResults();
      expect(scan.displayedResults.single.ip, '198.51.100.1');
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'a late history lookup cannot replace a new scan or a newer selection',
    () async {
      final bridge = HistoryBridge()
        ..records = [record('a'), record('b', ip: '192.0.2.2')];
      final scan = ScanController(bridge);
      await scan.initialize();
      final old = Completer<Map<String, dynamic>>();
      bridge.readEntry = (id) async => id == 'a' ? old.future : record('b');
      final selectingOld = scan.selectHistory('a');
      await scan.selectHistory('b');
      old.complete(record('a'));
      await selectingOld;
      expect(scan.selectedHistory?.id, 'b');

      final late = Completer<Map<String, dynamic>>();
      bridge.readEntry = (_) => late.future;
      final selectingAgain = scan.selectHistory('a');
      await scan.start();
      late.complete(record('a'));
      await selectingAgain;
      expect(scan.selectedHistory, isNull);
      expect(scan.historyEntryLoading, isFalse);
      expect(scan.running, isTrue);
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'history failures remain separate from scanning and can be retried',
    () async {
      final bridge = HistoryBridge()
        ..records = [record('saved')]
        ..failRead = true;
      final scan = ScanController(bridge);
      await scan.initialize();
      expect(scan.ready, isTrue);
      expect(scan.historyError, contains('读取历史失败'));
      expect(scan.error, isEmpty);
      await scan.start();
      expect(bridge.starts, 1);
      bridge.failRead = false;
      await scan.refreshHistory();
      expect(scan.history.single.id, 'saved');
      expect(scan.historyError, isEmpty);
      scan.applySnapshot({...bridge.state, 'historyError': '历史保存失败'});
      await scan.refreshHistory();
      expect(scan.historyError, '历史保存失败');
      scan.applySnapshot({...bridge.state, 'historyError': ''});
      expect(scan.historyError, isEmpty);
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'deletion preserves the live run and a failed deletion keeps the entry',
    () async {
      final bridge = HistoryBridge()..records = [record('saved')];
      final scan = ScanController(bridge);
      await scan.initialize();
      bridge.failDelete = true;
      await scan.deleteHistory('saved');
      expect(scan.selectedHistory?.id, 'saved');
      expect(scan.history, hasLength(1));
      expect(scan.historyError, contains('删除历史失败'));
      bridge.failDelete = false;
      await scan.start();
      await scan.selectHistory('saved');
      await scan.deleteHistory('saved');
      expect(scan.history, isEmpty);
      expect(scan.selectedHistory, isNull);
      expect(scan.historyError, isEmpty);
      expect(scan.running, isTrue);
      expect(bridge.stops, 0);
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'a revision arriving during history loading triggers a fresh read',
    () async {
      final bridge = HistoryBridge()..records = [record('old')];
      final scan = ScanController(bridge);
      await scan.initialize();
      final staleListing = bridge.listing;
      final pending = Completer<Map<String, dynamic>>();
      int reads = 0;
      bridge.read = () async => ++reads == 1 ? pending.future : bridge.listing;
      final refresh = scan.refreshHistory();
      bridge.records.insert(0, record('new'));
      scan.applySnapshot({'runId': 0, 'status': 'idle', 'historyRevision': 1});
      pending.complete(staleListing);
      await refresh;
      expect(reads, 2);
      expect(scan.history.first.id, 'new');
      expect(scan.historyLoading, isFalse);
      scan.dispose();
      await bridge.bus.close();
    },
  );

  test(
    'corrupt entry data is rejected instead of displaying a partial result',
    () {
      final inconsistent = record('broken')..['resultCount'] = 2;
      expect(
        () => ScanHistoryEntry.fromMap(inconsistent),
        throwsFormatException,
      );
      final malformed = record('broken');
      (malformed['results'] as List).first['score_ms'] = 'invalid';
      expect(() => ScanHistoryEntry.fromMap(malformed), throwsFormatException);
    },
  );
}
