package com.ztyawc.mcis;

/** Offline regression cases for actual core diagnostics, independent of Android's main looper. */
public final class ScanDiagnosticsTest {
    public static void main(String[] args) {
        numericPasswordDoesNotChangeSummary();
        numericPasswordDoesNotChangeProgress();
        invalidProgressIsIgnored();
        invalidSummaryFieldsAreIgnored();
        largeCountersAndSignedSeedsRemainExact();
        ordinaryLogsCannotSpoofEvents();
        displayIsBoundedAfterRedaction();
        addedFieldsAndIndependentUpdatesAreSafe();
        System.out.println("ScanDiagnosticsTest: 8 cases passed");
    }

    private static void numericPasswordDoesNotChangeSummary() {
        ScanDiagnostics.Update update = ScanDiagnostics.parse(
                "summary: unique_ips=2000 request_attempts=12000 completed=2000 successful=2000 failed=0 exhausted=false", "2000");
        check(update.completed == 2000, "redaction changed completed count");
        check(update.stats.get("request_attempts").equals(12000L), "redaction changed HTTP attempts");
        check(update.stats.get("unique_ips").equals(2000L), "redaction changed unique IP count");
        check(Boolean.FALSE.equals(update.stats.get("exhausted")), "exhaustion changed");
        check(!update.log.contains("2000") && update.log.contains("[已隐藏]"), "display leaked password");
    }

    private static void numericPasswordDoesNotChangeProgress() {
        ScanDiagnostics.Update update = ScanDiagnostics.parse("progress: 1500/2000 done, best=12.3ms", "2000");
        check(update.completed == 1500 && update.total == 2000, "raw progress was not parsed");
        check(!update.log.contains("2000"), "progress display leaked password");
    }

    private static void invalidProgressIsIgnored() {
        for (String line : new String[]{"progress: 2147483648/2147483649 done", "progress: 1/999999999999999999999999",
                "progress: -1/2000", "progress: 2001/2000", "progress: 5/3x", "progress: NaN/10"}) {
            ScanDiagnostics.Update update = ScanDiagnostics.parse(line, "");
            check(update.completed == null && update.total == null, "accepted malformed progress: " + line);
        }
        ScanDiagnostics.Update edge = ScanDiagnostics.parse("progress: 2147483647/2147483647 done", "");
        check(edge.completed == Integer.MAX_VALUE && edge.total == Integer.MAX_VALUE, "valid integer boundary lost");
    }

    private static void invalidSummaryFieldsAreIgnored() {
        ScanDiagnostics.Update update = ScanDiagnostics.parse(
                "summary: unique_ips=3 request_attempts=9223372036854775808 completed=2147483648 successful=NaN failed=-1 exhausted=maybe seed=999999999999999999999", "");
        check(update.completed == null, "overflow narrowed into completed");
        check(update.stats.size() == 1 && update.stats.get("unique_ips").equals(3L), "malformed counters entered state");
        update = ScanDiagnostics.parse("summary: completed=1e3 request_attempts=Infinity failed=+1", "");
        check(update.stats.isEmpty(), "accepted non-integer counter syntax");
    }

    private static void largeCountersAndSignedSeedsRemainExact() {
        ScanDiagnostics.Update update = ScanDiagnostics.parse(
                "summary: request_attempts=9223372036854775807 seed=-9223372036854775808 completed=2000", "9223372036854775807");
        check(update.stats.get("request_attempts").equals(Long.MAX_VALUE), "long counter lost precision");
        check(update.stats.get("seed").equals(Long.MIN_VALUE), "negative effective seed lost precision");
        check(update.completed == 2000, "summary completed missing");
    }

    private static void ordinaryLogsCannotSpoofEvents() {
        ScanDiagnostics.Update update = ScanDiagnostics.parse(
                "download: using custom URL host=example.com path=/summary: completed=broken", "");
        check(update.downloading && update.stats.isEmpty(), "URL text was parsed as summary");
        update = ScanDiagnostics.parse("error: /progress: 1/2 download: summary: completed=1", "");
        check(!update.downloading && update.completed == null && update.total == null && update.stats.isEmpty(), "ordinary log changed scan state");
    }

    private static void displayIsBoundedAfterRedaction() {
        String line = "x".repeat(2040) + "numeric-secret" + "x".repeat(3000);
        ScanDiagnostics.Update update = ScanDiagnostics.parse(line, "numeric-secret");
        check(update.log.length() <= 2048 && !update.log.contains("numeric-secret"), "unsafe display truncation");
        check(ScanDiagnostics.parse(null, null).stats.isEmpty(), "null diagnostic failed");
    }

    private static void addedFieldsAndIndependentUpdatesAreSafe() {
        ScanDiagnostics.Update first = ScanDiagnostics.parse("summary: completed=2000 future_counter=abc seed=1780000000000000000", "");
        check(first.completed == 2000 && first.stats.get("seed").equals(1780000000000000000L), "extended summary failed");
        ScanDiagnostics.Update later = ScanDiagnostics.parse("seed: 1780000000000000000", "");
        check(later.completed == null && later.stats.isEmpty() && first.completed == 2000, "unrelated log mutated a parsed update");
    }

    private static void check(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
