package com.ztyawc.mcis;

import java.io.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.util.*;

/** Real-file persistence/restart/failure tests. No Android runtime, emulators or network. */
public final class ScanHistoryStoreTest {
    private static Path directory;
    private static int passed;

    public static void main(String[] args) throws Exception {
        directory = Files.createTempDirectory("mcis-history-tests-");
        try {
            emptyHistoryDoesNotWrite();
            completedResultsSurviveRestart();
            allTerminalStatusesSurviveRestart();
            pendingIsHiddenAndRecoveredAsInterrupted();
            completionReplacesPendingWithoutDuplication();
            distinctRunsRemainDistinctAfterRestart();
            mostRecentFiftyAreRetained();
            deletingOneRecordPreservesOthers();
            failedSavePreservesFileAndMemory();
            failedDeletePreservesFileAndMemory();
            failedFirstSaveLeavesNoPartialHistory();
            corruptHistoryCannotBeOverwritten();
            unsupportedVersionCannotBeOverwritten();
            oversizedCountCannotBeOverwritten();
            trailingDataCannotBeOverwritten();
            privacyWhitelistRemovesSensitiveFields();
            callerMutationCannotAlterStoredEntries();
            invalidIdsCannotAddressFiles();
            fullTopThousandIsPreserved();
            tooManyResultsAreRejected();
            symbolicLinkHistoryIsRejected();
            invalidNumericFieldsAreOmitted();
            recoveryReadFailureRemainsLockedUntilRestart();
            pendingCannotBeDeleted();
            replacementReceivesCompleteClosedFileInSameDirectory();
            explicitReadRetryUnlocksOnlyAfterValidation();
            failedFinalSaveRevealsLastCheckpoint();
            successfulReadCannotResolveFailedFinalSave();
            internationalHostIsSavedAsAscii();
            onlySuccessfulValidIpRowsAreSaved();
            System.out.println("ScanHistoryStoreTest: " + passed + " real-file cases passed");
        } finally {
            try (java.util.stream.Stream<Path> paths = Files.walk(directory)) {
                for (Path path : (Iterable<Path>) paths.sorted(Comparator.reverseOrder())::iterator) Files.deleteIfExists(path);
            }
        }
    }

    private static File file(String name) { return directory.resolve(name + ".bin").toFile(); }
    private static String id(int number) { return new UUID(0, number + 1).toString(); }
    private static ScanHistoryStore.Entry entry(int number, String status) {
        return ScanHistoryStore.capture(id(number), 1000L + number, 2000L + number, status, 4, "www.cloudflare.com",
                2000, 1500, 2000, Collections.singletonList(row()), Collections.singletonMap("successful", 123L));
    }
    private static Map<String, Object> row() {
        Map<String, Object> row = new LinkedHashMap<>();
        row.put("ip", "1.1.1.1"); row.put("prefix", "1.1.1.0/24"); row.put("ok", true); row.put("status", 200);
        row.put("connect_ms", 12L); row.put("tls_ms", 23L); row.put("ttfb_ms", 34L); row.put("total_ms", 69L); row.put("score_ms", 22.5);
        row.put("trace", new LinkedHashMap<>(Collections.singletonMap("colo", "HKG")));
        row.put("download_ok", true); row.put("download_bytes", 10_000_000L); row.put("download_ms", 1200L); row.put("download_mbps", 66.66);
        return row;
    }

    private static void emptyHistoryDoesNotWrite() throws Exception {
        File path = file("empty"); ScanHistoryStore store = new ScanHistoryStore(path);
        check(store.summaries().isEmpty() && !path.exists(), "loading an empty history wrote a file"); passed++;
    }

    private static void completedResultsSurviveRestart() throws Exception {
        File path = file("restart"); ScanHistoryStore store = new ScanHistoryStore(path);
        ScanHistoryStore.Entry entry = entry(1, "completed"); store.save(entry, false);
        ScanHistoryStore restarted = new ScanHistoryStore(path);
        check(restarted.entry(entry.id).equals(entry.full()), "restarted result details differ");
        Map<String, Object> summary = restarted.summaries().get(0);
        check(summary.get("resultCount").equals(1) && !summary.containsKey("results") && !summary.containsKey("stats"), "list loaded full result data");
        check(summary.get("startedAt").equals(1001L) && summary.get("finishedAt").equals(2001L), "timestamps changed"); passed++;
    }

    private static void allTerminalStatusesSurviveRestart() throws Exception {
        File path = file("statuses"); ScanHistoryStore store = new ScanHistoryStore(path);
        int index = 1;
        for (String status : Arrays.asList("completed", "stopped", "error", "interrupted")) {
            ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(index), index, index + 10, status, 6, "example.com",
                    100, 0, 100, Collections.emptyList(), Collections.emptyMap());
            store.save(entry, false); index++;
        }
        ScanHistoryStore restarted = new ScanHistoryStore(path);
        check(restarted.summaries().size() == 4, "empty or interrupted run disappeared");
        for (Map<String, Object> item : restarted.summaries()) check(item.get("resultCount").equals(0) && item.get("ipVersion").equals(6), "empty IPv6 summary changed");
        passed++;
    }

    private static void pendingIsHiddenAndRecoveredAsInterrupted() throws Exception {
        File path = file("pending"); ScanHistoryStore store = new ScanHistoryStore(path);
        store.save(entry(1, "completed"), false); store.save(entry(2, "interrupted"), true);
        check(store.summaries().size() == 1 && store.summaries().get(0).get("id").equals(id(1)), "active run was labeled interrupted while still running");
        ScanHistoryStore restarted = new ScanHistoryStore(path);
        check(restarted.summaries().size() == 2 && restarted.entry(id(2)).get("status").equals("interrupted"), "unfinished run was not recovered");
        check(((List<?>) restarted.entry(id(2)).get("results")).size() == 1, "partial results disappeared on restart"); passed++;
    }

    private static void completionReplacesPendingWithoutDuplication() throws Exception {
        File path = file("deduplicate"); ScanHistoryStore store = new ScanHistoryStore(path);
        store.save(entry(1, "interrupted"), true); store.save(entry(1, "interrupted"), true);
        store.save(entry(1, "completed"), false); store.save(entry(1, "completed"), false);
        check(store.summaries().size() == 1 && new ScanHistoryStore(path).summaries().size() == 1, "same scan was duplicated");
        check(store.entry(id(1)).get("status").equals("completed"), "pending status overwrote completion"); passed++;
    }

    private static void distinctRunsRemainDistinctAfterRestart() throws Exception {
        File path = file("different-runs"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        ScanHistoryStore restarted = new ScanHistoryStore(path); restarted.save(entry(2, "stopped"), false);
        check(new ScanHistoryStore(path).summaries().size() == 2, "new run replaced previous process's run"); passed++;
    }

    private static void mostRecentFiftyAreRetained() throws Exception {
        File path = file("retention"); ScanHistoryStore store = new ScanHistoryStore(path);
        for (int i = 53; i >= 0; i--) store.save(entry(i, "completed"), false);
        List<Map<String, Object>> history = new ScanHistoryStore(path).summaries();
        check(history.size() == ScanHistoryStore.LIMIT, "history limit was not enforced");
        check(history.get(0).get("id").equals(id(53)) && history.get(49).get("id").equals(id(4)), "retention followed write order rather than scan date"); passed++;
    }

    private static void deletingOneRecordPreservesOthers() throws Exception {
        File path = file("delete"); ScanHistoryStore store = new ScanHistoryStore(path);
        store.save(entry(1, "completed"), false); store.save(entry(2, "stopped"), false);
        Map<String, Object> before = store.entry(id(1));
        check(store.delete(id(2)) && !store.delete(id(2)), "deletion was not idempotent");
        ScanHistoryStore restarted = new ScanHistoryStore(path);
        check(restarted.summaries().size() == 1 && restarted.entry(id(1)).equals(before), "deletion changed unrelated history");
        expect(IOException.class, () -> restarted.entry(id(2))); passed++;
    }

    private static void failedSavePreservesFileAndMemory() throws Exception {
        File path = file("write-failure"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] before = Files.readAllBytes(path.toPath()); ScanHistoryStore failing = failureStore(path);
        expect(IOException.class, () -> failing.save(entry(2, "completed"), false));
        check(Arrays.equals(before, Files.readAllBytes(path.toPath())), "atomic replacement failure changed existing file");
        check(failing.summaries().size() == 1 && new ScanHistoryStore(path).summaries().size() == 1, "failed save changed visible history");
        assertNoTemporaryFiles(); passed++;
    }

    private static void failedDeletePreservesFileAndMemory() throws Exception {
        File path = file("delete-failure"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] before = Files.readAllBytes(path.toPath()); ScanHistoryStore failing = failureStore(path);
        expect(IOException.class, () -> failing.delete(id(1)));
        check(Arrays.equals(before, Files.readAllBytes(path.toPath())) && failing.summaries().size() == 1, "failed delete lost a record"); passed++;
    }

    private static void failedFirstSaveLeavesNoPartialHistory() throws Exception {
        File path = file("first-failure"); ScanHistoryStore failing = failureStore(path);
        expect(IOException.class, () -> failing.save(entry(1, "completed"), false));
        check(!path.exists() && failing.summaries().isEmpty(), "failed initial save left partial history"); assertNoTemporaryFiles(); passed++;
    }

    private static void corruptHistoryCannotBeOverwritten() throws Exception {
        File path = file("truncated"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] bytes = Files.readAllBytes(path.toPath()); assertUnreadablePreserved(path, Arrays.copyOf(bytes, bytes.length / 2)); passed++;
    }

    private static void unsupportedVersionCannotBeOverwritten() throws Exception {
        File path = file("version"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] bytes = Files.readAllBytes(path.toPath()); bytes[7] = 100; assertUnreadablePreserved(path, bytes); passed++;
    }

    private static void oversizedCountCannotBeOverwritten() throws Exception {
        File path = file("count"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] bytes = Files.readAllBytes(path.toPath()); bytes[11] = 100; assertUnreadablePreserved(path, bytes); passed++;
    }

    private static void trailingDataCannotBeOverwritten() throws Exception {
        File path = file("trailing"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] bytes = Files.readAllBytes(path.toPath()); assertUnreadablePreserved(path, Arrays.copyOf(bytes, bytes.length + 1)); passed++;
    }

    private static void privacyWhitelistRemovesSensitiveFields() throws Exception {
        File path = file("privacy"); Map<String, Object> source = row();
        source.put("download_error", "proxy-user-private proxy-secret-private https://download.invalid/private?token=secret-query");
        source.put("error", "error-secret"); source.put("proxy_password", "password-secret"); source.put("download_url", "url-secret");
        source.put("logs", Arrays.asList("log-secret")); source.put("proxy_address", "proxy-address-secret"); source.put("proxy_username", "username-secret");
        source.put("trace", Map.of("colo", "HKG", "ip", "client-ip-secret", "loc", "location-secret", "url", "trace-secret"));
        Map<String, Object> stats = new HashMap<>(); stats.put("successful", 22L); stats.put("seed", Long.MIN_VALUE); stats.put("exhausted", true);
        stats.put("proxy_password", "stat-secret"); stats.put("download_url", "stat-url-secret");
        ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "error", 4,
                "https://user-secret:password-secret@host.invalid/private?token=host-secret", 100, 99, 100, Collections.singletonList(source), stats);
        ScanHistoryStore store = new ScanHistoryStore(path); store.save(entry, false);
        String bytes = new String(Files.readAllBytes(path.toPath()), StandardCharsets.ISO_8859_1);
        for (String secret : Arrays.asList("proxy-user-private", "proxy-secret-private", "download.invalid", "secret-query", "error-secret", "password-secret", "url-secret",
                "log-secret", "proxy-address-secret", "username-secret", "client-ip-secret", "location-secret", "trace-secret", "stat-secret", "stat-url-secret", "host-secret"))
            check(!bytes.contains(secret), "history leaked " + secret);
        Map<String, Object> persisted = new ScanHistoryStore(path).entry(id(1));
        @SuppressWarnings("unchecked") Map<String, Object> row = (Map<String, Object>) ((List<?>) persisted.get("results")).get(0);
        check(row.get("trace").equals(Collections.singletonMap("colo", "HKG")), "trace whitelist changed");
        check(((String) row.get("download_error")).startsWith("测速失败") && persisted.get("host").equals(""), "unsafe host or error retained");
        check(((Map<?, ?>) persisted.get("stats")).get("seed").equals(Long.MIN_VALUE), "seed lost precision"); passed++;
    }

    private static void callerMutationCannotAlterStoredEntries() throws Exception {
        File path = file("immutable"); Map<String, Object> source = row(); List<Map<String, Object>> rows = new ArrayList<>(); rows.add(source);
        ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "completed", 4, "example.com", 100, 100, 100, rows, new HashMap<>());
        source.clear(); rows.clear(); ScanHistoryStore store = new ScanHistoryStore(path); store.save(entry, false);
        Map<String, Object> copy = store.entry(id(1));
        @SuppressWarnings("unchecked") Map<String, Object> returnedRow = (Map<String, Object>) ((List<?>) copy.get("results")).get(0);
        @SuppressWarnings("unchecked") Map<String, Object> trace = (Map<String, Object>) returnedRow.get("trace");
        trace.put("colo", "MUT"); returnedRow.clear(); copy.clear();
        check(store.entry(id(1)).equals(entry.full()) && new ScanHistoryStore(path).entry(id(1)).equals(entry.full()), "caller mutated stored history"); passed++;
    }

    private static void invalidIdsCannotAddressFiles() throws Exception {
        ScanHistoryStore store = new ScanHistoryStore(file("ids"));
        for (String invalid : Arrays.asList("../other-file", "", "1", "/data/secret")) {
            expect(IllegalArgumentException.class, () -> store.entry(invalid)); expect(IllegalArgumentException.class, () -> store.delete(invalid));
        }
        passed++;
    }

    private static void fullTopThousandIsPreserved() throws Exception {
        File path = file("thousand"); List<Map<String, Object>> rows = new ArrayList<>();
        for (int i = 0; i < 1000; i++) { Map<String, Object> row = row(); row.put("ip", "192.0." + (i / 256) + "." + (i % 256)); rows.add(row); }
        ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "completed", 4, "example.com", 2000, 2000, 2000, rows, Collections.emptyMap());
        new ScanHistoryStore(path).save(entry, false);
        check(new ScanHistoryStore(path).entry(id(1)).equals(entry.full()), "large result set was truncated or reordered"); passed++;
    }

    private static void tooManyResultsAreRejected() throws Exception {
        expect(IllegalArgumentException.class, () -> ScanHistoryStore.capture(id(1), 1, 2, "completed", 4, "example.com", 2000, 2000, 2000,
                Collections.nCopies(1001, row()), Collections.emptyMap())); passed++;
    }

    private static void symbolicLinkHistoryIsRejected() throws Exception {
        File original = file("link-target"); new ScanHistoryStore(original).save(entry(1, "completed"), false);
        Path link = file("link").toPath(); Files.createSymbolicLink(link, original.toPath()); byte[] before = Files.readAllBytes(original.toPath());
        ScanHistoryStore store = new ScanHistoryStore(link.toFile()); expect(IOException.class, store::load);
        expect(IOException.class, () -> store.save(entry(2, "completed"), false));
        check(Files.isSymbolicLink(link) && Arrays.equals(before, Files.readAllBytes(original.toPath())), "symlink target changed"); passed++;
    }

    private static void invalidNumericFieldsAreOmitted() throws Exception {
        File path = file("numbers"); Map<String, Object> row = row(); row.put("score_ms", Double.NaN); row.put("download_mbps", Double.POSITIVE_INFINITY);
        row.put("connect_ms", -1L); row.put("total_ms", "password-secret");
        ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "completed", 4, "example.com", 100, 100, 100,
                Collections.singletonList(row), Map.of("completed", "100", "failed", -1L, "seed", Long.MAX_VALUE));
        new ScanHistoryStore(path).save(entry, false); Map<String, Object> full = new ScanHistoryStore(path).entry(id(1));
        Map<?, ?> persisted = (Map<?, ?>) ((List<?>) full.get("results")).get(0);
        for (String key : Arrays.asList("score_ms", "download_mbps", "connect_ms", "total_ms")) check(!persisted.containsKey(key), "unsafe numeric field saved");
        check(((Map<?, ?>) full.get("stats")).equals(Collections.singletonMap("seed", Long.MAX_VALUE)), "invalid statistic retained"); passed++;
    }

    private static void recoveryReadFailureRemainsLockedUntilRestart() throws Exception {
        File path = file("locked"); new ScanHistoryStore(path).save(entry(1, "completed"), false); byte[] healthy = Files.readAllBytes(path.toPath());
        Files.write(path.toPath(), new byte[]{1}); ScanHistoryStore store = new ScanHistoryStore(path); expect(IOException.class, store::load);
        Files.write(path.toPath(), healthy); expect(IOException.class, () -> store.save(entry(2, "completed"), false));
        check(Arrays.equals(healthy, Files.readAllBytes(path.toPath())), "read-failed store wrote after losing its state");
        check(new ScanHistoryStore(path).summaries().size() == 1, "restart could not read repaired file"); passed++;
    }

    private static void pendingCannotBeDeleted() throws Exception {
        File path = file("pending-delete"); ScanHistoryStore store = new ScanHistoryStore(path); store.save(entry(1, "interrupted"), true);
        byte[] before = Files.readAllBytes(path.toPath()); expect(IllegalArgumentException.class, () -> store.delete(id(1)));
        check(Arrays.equals(before, Files.readAllBytes(path.toPath())), "running record was deleted"); passed++;
    }

    private static void replacementReceivesCompleteClosedFileInSameDirectory() throws Exception {
        File path = file("atomic-contract"); boolean[] called = {false};
        ScanHistoryStore store = new ScanHistoryStore(path, (temporary, destination) -> {
            check(temporary.getParent().equals(destination.getParent()), "temporary file is not on the same filesystem");
            check(new ScanHistoryStore(temporary.toFile()).entry(id(1)).equals(entry(1, "completed").full()), "replacement received incomplete data");
            Files.move(temporary, destination, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING); called[0] = true;
        });
        store.save(entry(1, "completed"), false); check(called[0], "atomic replacement was skipped"); assertNoTemporaryFiles(); passed++;
    }

    private static void explicitReadRetryUnlocksOnlyAfterValidation() throws Exception {
        File path = file("read-retry"); new ScanHistoryStore(path).save(entry(1, "completed"), false);
        byte[] healthy = Files.readAllBytes(path.toPath()); Files.write(path.toPath(), new byte[]{1, 2, 3});
        ScanHistoryStore store = new ScanHistoryStore(path); expect(IOException.class, store::load);
        expect(IOException.class, store::retryRead); expect(IOException.class, () -> store.save(entry(2, "completed"), false));
        check(Arrays.equals(Files.readAllBytes(path.toPath()), new byte[]{1, 2, 3}), "read retry replaced corrupt data");
        Files.write(path.toPath(), healthy); check(store.retryRead(), "explicit retry did not recover after readable file was restored");
        check(store.summaries().size() == 1 && !store.retryRead(), "read retry lost the previous record or repeatedly reloaded");
        store.save(entry(2, "completed"), false); check(new ScanHistoryStore(path).summaries().size() == 2, "validated retry did not unlock writes"); passed++;
    }

    private static void failedFinalSaveRevealsLastCheckpoint() throws Exception {
        File path = file("reveal-failed-final"); boolean[] fail = {false};
        ScanHistoryStore store = new ScanHistoryStore(path, (temporary, destination) -> {
            if (fail[0]) throw new IOException("simulated final replacement failure");
            Files.move(temporary, destination, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING);
        });
        store.save(entry(1, "completed"), false); store.save(entry(2, "interrupted"), true); byte[] before = Files.readAllBytes(path.toPath());
        fail[0] = true; expect(IOException.class, () -> store.save(entry(2, "completed"), false));
        check(store.revealFailedFinalSave(id(2)) && !store.revealFailedFinalSave(id(2)), "failed final save did not reveal its checkpoint exactly once");
        check(store.hasFailedFinalSaves() && store.summaries().size() == 2 && store.entry(id(2)).get("status").equals("interrupted"), "same-process recreation cannot see interrupted checkpoint");
        check(Arrays.equals(before, Files.readAllBytes(path.toPath())), "revealing checkpoint changed file after failed save");
        check(new ScanHistoryStore(path).entry(id(2)).get("status").equals("interrupted"), "restart lost failed run's saved checkpoint");
        fail[0] = false; store.save(entry(2, "completed"), false);
        check(!store.hasFailedFinalSaves() && store.summaries().size() == 2 && store.entry(id(2)).get("status").equals("completed"), "successful retry did not resolve failed final save"); passed++;
    }

    private static void successfulReadCannotResolveFailedFinalSave() throws Exception {
        File path = file("keep-save-error"); ScanHistoryStore store = new ScanHistoryStore(path);
        store.save(entry(1, "interrupted"), true); store.revealFailedFinalSave(id(1));
        store.retryRead(); store.summaries(); store.entry(id(1));
        check(store.hasFailedFinalSaves(), "successful history read cleared unresolved save failure");
        store.save(entry(2, "completed"), false);
        check(store.hasFailedFinalSaves(), "different run cleared unresolved save failure");
        store.delete(id(1)); check(!store.hasFailedFinalSaves(), "deleting failed record did not resolve its warning"); passed++;
    }

    private static void internationalHostIsSavedAsAscii() throws Exception {
        File path = file("international-host"); ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "completed", 4,
                "例子.测试", 100, 100, 100, Collections.singletonList(row()), Collections.emptyMap());
        new ScanHistoryStore(path).save(entry, false);
        check(new ScanHistoryStore(path).entry(id(1)).get("host").equals("xn--fsqu00a.xn--0zwm56d"), "internationalized host was lost"); passed++;
    }

    private static void onlySuccessfulValidIpRowsAreSaved() throws Exception {
        File path = file("valid-rows"); List<Map<String, Object>> rows = new ArrayList<>();
        for (String ip : Arrays.asList("1.1.1.1", "2606:4700:4700::1111", "::ffff:192.0.2.1", "deadbeef", "1.2.3", "1.2.3.999", "1::2::3", "1:2:3:4:5:6:7:8:9", "1.01.2.3")) {
            Map<String, Object> row = row(); row.put("ip", ip); rows.add(row);
        }
        Map<String, Object> failed = row(); failed.put("ok", false); rows.add(failed);
        Map<String, Object> missingOk = row(); missingOk.remove("ok"); rows.add(missingOk);
        ScanHistoryStore.Entry entry = ScanHistoryStore.capture(id(1), 1, 2, "completed", 4, "example.com", 100, 100, 100, rows, Collections.emptyMap());
        new ScanHistoryStore(path).save(entry, false);
        Map<String, Object> full = new ScanHistoryStore(path).entry(id(1));
        check(full.get("resultCount").equals(3) && ((List<?>) full.get("results")).size() == 3, "failed or malformed IP row was retained"); passed++;
    }

    private static ScanHistoryStore failureStore(File path) {
        return new ScanHistoryStore(path, (temporary, destination) -> { throw new IOException("simulated atomic replacement failure"); });
    }

    private static void assertUnreadablePreserved(File path, byte[] bytes) throws Exception {
        Files.write(path.toPath(), bytes); ScanHistoryStore store = new ScanHistoryStore(path);
        expect(IOException.class, store::load); expect(IOException.class, () -> store.save(entry(2, "completed"), false));
        expect(IOException.class, () -> store.delete(id(1)));
        check(Arrays.equals(bytes, Files.readAllBytes(path.toPath())), "unreadable history was overwritten");
    }

    private static void assertNoTemporaryFiles() throws IOException {
        try (java.util.stream.Stream<Path> files = Files.list(directory)) {
            check(files.noneMatch(path -> path.getFileName().toString().endsWith(".tmp")), "temporary history file was retained");
        }
    }

    private interface Task { void run() throws Exception; }
    private static void expect(Class<? extends Exception> type, Task task) throws Exception {
        try { task.run(); } catch (Exception failure) { if (type.isInstance(failure)) return; throw failure; }
        throw new AssertionError("expected " + type.getSimpleName());
    }
    private static void check(boolean condition, String message) { if (!condition) throw new AssertionError(message); }
}
