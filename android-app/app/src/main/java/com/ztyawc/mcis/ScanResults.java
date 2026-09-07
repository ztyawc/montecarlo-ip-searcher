package com.ztyawc.mcis;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/** Complete structured results. Paging limits rendering, never the stored or copied ranking. */
final class ScanResults {
    static final int PAGE_SIZE = 50;
    private final List<Row> rows = new ArrayList<>();

    static final class Row {
        final String ip, prefix, colo, downloadError;
        final double scoreMS, downloadMbps;
        final int status;
        final boolean downloadOK;
        final long downloadMS, downloadBytes;

        Row(String ip, String prefix, String colo, double scoreMS, int status,
                boolean downloadOK, double downloadMbps, long downloadMS, long downloadBytes, String downloadError) {
            if (ip == null || ip.isEmpty() || !Double.isFinite(scoreMS) || scoreMS < 0
                    || downloadOK && (!Double.isFinite(downloadMbps) || downloadMbps < 0)) {
                throw new IllegalArgumentException("无效扫描结果");
            }
            this.ip = cell(ip);
            this.prefix = cell(prefix);
            this.colo = cell(colo);
            this.scoreMS = scoreMS;
            this.status = status;
            this.downloadOK = downloadOK;
            this.downloadMbps = downloadOK ? downloadMbps : 0;
            this.downloadMS = downloadMS;
            this.downloadBytes = downloadBytes;
            this.downloadError = cell(downloadError);
        }

        String text(int rank) {
            String line = String.format(Locale.US, "%d\t%s\t%.1fms\tok=true\tstatus=%d\tprefix=%s\tcolo=%s",
                    rank, ip, scoreMS, status, prefix, colo);
            if (downloadOK || !downloadError.isEmpty() || downloadMS != 0 || downloadBytes != 0) {
                line += String.format(Locale.US, "\tdl_ok=%s\tdl_mbps=%.2f\tdl_ms=%d", downloadOK, downloadMbps, downloadMS);
                if (!downloadError.isEmpty()) line += "\tdl_err=" + downloadError;
            }
            return line + '\n';
        }

        private static String cell(String value) {
            return value == null ? "" : value.replace('\r', ' ').replace('\n', ' ').replace('\t', ' ');
        }
    }

    void add(Row row) { rows.add(row); }
    int size() { return rows.size(); }
    void clear() { rows.clear(); }
    int pageCount() { return (size() + PAGE_SIZE - 1) / PAGE_SIZE; }
    int clampPage(int page) { return Math.max(0, Math.min(page, pageCount() - 1)); }

    String pageText(int page) {
        int start = clampPage(page) * PAGE_SIZE;
        return text(start, Math.min(size(), start + PAGE_SIZE));
    }

    String copyText() { return text(0, size()); }

    private String text(int start, int end) {
        StringBuilder output = new StringBuilder();
        for (int i = start; i < end; i++) output.append(rows.get(i).text(i + 1));
        return output.toString();
    }
}
