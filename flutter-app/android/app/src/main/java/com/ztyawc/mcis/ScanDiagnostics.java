package com.ztyawc.mcis;

import java.util.Collections;
import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Parses the core's original diagnostic line; redaction belongs only to its display copy. */
final class ScanDiagnostics {
    private static final Pattern PROGRESS = Pattern.compile("^progress:\\s+([0-9]{1,10})/([0-9]{1,10})(?:\\s|$)");
    private static final Pattern STAT = Pattern.compile("(?:^|\\s)(unique_ips|request_attempts|completed|successful|failed|exhausted|seed)=([^\\s]+)");

    static final class Update {
        final String log;
        final Integer completed, total;
        final boolean downloading;
        final Map<String, Object> stats;

        Update(String log, Integer completed, Integer total, boolean downloading, Map<String, Object> stats) {
            this.log = log;
            this.completed = completed;
            this.total = total;
            this.downloading = downloading;
            this.stats = Collections.unmodifiableMap(stats);
        }
    }

    static Update parse(String line, String password) {
        String raw = line == null ? "" : line;
        Integer completed = null, total = null;
        Map<String, Object> stats = new HashMap<>();
        Matcher progress = PROGRESS.matcher(raw);
        if (progress.find()) {
            Long done = number(progress.group(1), false), budget = number(progress.group(2), false);
            if (done != null && budget != null && done <= budget && budget <= Integer.MAX_VALUE) {
                completed = done.intValue();
                total = budget.intValue();
            }
        }
        // Diagnostic text such as a download URL must not be interpreted as a summary.
        if (raw.startsWith("summary:")) {
            Matcher stat = STAT.matcher(raw.substring("summary:".length()));
            while (stat.find()) {
                String key = stat.group(1), value = stat.group(2);
                if (key.equals("exhausted")) {
                    if (value.equals("true") || value.equals("false")) stats.put(key, Boolean.parseBoolean(value));
                } else {
                    Long n = number(value, key.equals("seed"));
                    if (n == null || key.equals("completed") && n > Integer.MAX_VALUE) continue;
                    stats.put(key, n);
                    if (key.equals("completed")) completed = n.intValue();
                }
            }
        }
        String log = redact(raw, password);
        if (log.length() > 2048) log = log.substring(0, 2048);
        return new Update(log, completed, total, raw.startsWith("download:"), stats);
    }

    private static Long number(String value, boolean signed) {
        int start = signed && value.startsWith("-") ? 1 : 0;
        if (value.length() == start || value.length() - start > 19) return null;
        for (int i = start; i < value.length(); i++) {
            if (value.charAt(i) < '0' || value.charAt(i) > '9') return null;
        }
        try { return Long.parseLong(value); }
        catch (NumberFormatException ignored) { return null; }
    }

    static String redact(String line, String password) {
        if (line == null) return "未知错误";
        return password == null || password.isEmpty() ? line : line.replace(password, "[已隐藏]");
    }
}
