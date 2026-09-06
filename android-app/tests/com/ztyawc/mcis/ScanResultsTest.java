package com.ztyawc.mcis;

import java.util.Locale;

/** Offline result retention and paging regression tests; no Android runtime or network. */
public final class ScanResultsTest {
    public static void main(String[] args) {
        thousandIPv6ResultsRetainTheBestRanking();
        everyPageContainsWholeRowsAndGlobalRanks();
        copyDoesNotDependOnTheDisplayedPage();
        clearStartsANewRanking();
        textFormatAndFailedDownloadRemainHonest();
        invalidRowsFailBeforeRendering();
        System.out.println("ScanResultsTest: 6 cases passed (including complete 1000-row IPv6 ranking)");
    }

    private static ScanResults thousandRows() {
        ScanResults result = new ScanResults();
        for (int rank = 1; rank <= 1000; rank++) {
            result.add(new ScanResults.Row(ip(rank), "2001:db8::/32", "HKG", rank, 200, false, 0, 0, 0, ""));
        }
        return result;
    }

    private static String ip(int rank) { return "2001:db8:ffff:ffff:ffff:ffff:ffff:" + Integer.toHexString(rank); }

    private static void thousandIPv6ResultsRetainTheBestRanking() {
        ScanResults result = thousandRows();
        String copied = result.copyText();
        check(result.size() == 1000 && copied.length() > 80000, "did not cover the former truncation boundary");
        String[] lines = copied.split("\n");
        check(lines.length == 1000 && lines[0].startsWith("1\t" + ip(1) + "\t"), "best result was lost");
        for (int rank = 1; rank <= 1000; rank++) {
            check(lines[rank - 1].startsWith(rank + "\t" + ip(rank) + "\t"), "ranking missing or truncated at " + rank);
        }
        check(!copied.contains("已省略"), "result data was treated as a rotating log");
    }

    private static void everyPageContainsWholeRowsAndGlobalRanks() {
        ScanResults result = thousandRows();
        check(result.pageCount() == 20, "wrong page count");
        StringBuilder joined = new StringBuilder();
        for (int page = 0; page < result.pageCount(); page++) {
            String text = result.pageText(page);
            check(text.split("\n").length == ScanResults.PAGE_SIZE, "page exceeded rendering limit");
            check(text.startsWith((page * ScanResults.PAGE_SIZE + 1) + "\t"), "page lost global rank");
            joined.append(text);
        }
        check(joined.toString().equals(result.copyText()), "paging lost, duplicated or split rows");
        check(result.pageText(-1).equals(result.pageText(0)), "negative page not clamped");
        check(result.pageText(Integer.MAX_VALUE).equals(result.pageText(19)), "last page not clamped");
    }

    private static void copyDoesNotDependOnTheDisplayedPage() {
        ScanResults result = thousandRows();
        String all = result.copyText();
        result.pageText(8);
        result.pageText(19);
        check(all.equals(result.copyText()), "display paging changed copied data");
        result.add(new ScanResults.Row(ip(1001), "2001:db8::/32", "HKG", 1001, 200, false, 0, 0, 0, ""));
        check(result.pageCount() == 21 && result.pageText(20).split("\n").length == 1, "partial final page incorrect");
    }

    private static void clearStartsANewRanking() {
        ScanResults result = thousandRows();
        result.clear();
        check(result.size() == 0 && result.pageCount() == 0 && result.copyText().isEmpty() && result.pageText(20).isEmpty(), "clear retained a previous run");
        result.add(new ScanResults.Row("192.0.2.1", "192.0.2.0/24", "HKG", 25, 200, false, 0, 0, 0, ""));
        check(result.pageText(20).startsWith("1\t192.0.2.1\t"), "new run did not reset rank");
    }

    private static void textFormatAndFailedDownloadRemainHonest() {
        Locale previous = Locale.getDefault();
        try {
            Locale.setDefault(Locale.GERMANY);
            ScanResults.Row row = new ScanResults.Row("192.0.2.1", "192.0.2.0/24", "HKG", 25, 200, true, 80, 100, 1000000, "");
            check(row.text(1).equals("1\t192.0.2.1\t25.0ms\tok=true\tstatus=200\tprefix=192.0.2.0/24\tcolo=HKG\tdl_ok=true\tdl_mbps=80.00\tdl_ms=100\n"), "copy format changed with locale");
            row = new ScanResults.Row("192.0.2.2", "192.0.2.0/24", "HKG", 26, 200, false, 999, 100, 5, "short_download\nextra\ttext");
            check(row.text(2).contains("dl_ok=false\tdl_mbps=0.00") && row.text(2).split("\n").length == 1, "failed download or line boundaries misrepresented");
        } finally { Locale.setDefault(previous); }
    }

    private static void invalidRowsFailBeforeRendering() {
        try {
            new ScanResults.Row("192.0.2.1", "", "", Double.NaN, 200, false, 0, 0, 0, "");
            throw new AssertionError("NaN score accepted");
        } catch (IllegalArgumentException expected) { }
        try {
            new ScanResults.Row("192.0.2.1", "", "", 1, 200, true, Double.POSITIVE_INFINITY, 1, 1, "");
            throw new AssertionError("infinite successful speed accepted");
        } catch (IllegalArgumentException expected) { }
    }

    private static void check(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
