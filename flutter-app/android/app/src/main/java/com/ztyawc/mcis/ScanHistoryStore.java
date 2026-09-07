package com.ztyawc.mcis;

import java.io.*;
import java.net.IDN;
import java.nio.file.*;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.*;

/** Private, bounded history. Call only from the scanner's serial history executor. */
final class ScanHistoryStore {
    static final int LIMIT = 50;
    private static final int MAGIC = 0x4d434853; // MCHS
    private static final int VERSION = 1;
    private static final int MAX_RESULTS = 1000;
    private static final long MAX_FILE_BYTES = 64L * 1024 * 1024;
    private static final Set<String> TERMINAL = new HashSet<>(Arrays.asList("completed", "stopped", "error", "interrupted"));
    private static final List<String> RESULT_NUMBERS = Arrays.asList("status", "connect_ms", "tls_ms", "ttfb_ms", "total_ms", "score_ms",
            "download_bytes", "download_ms", "download_mbps", "prefix_samples", "prefix_ok", "prefix_fail");
    private static final List<String> STAT_NUMBERS = Arrays.asList("unique_ips", "request_attempts", "completed", "successful", "failed", "seed");
    private static final String DOWNLOAD_FAILURE = "测速失败（历史记录不保留原始错误详情）";

    interface AtomicReplace { void replace(Path temporary, Path destination) throws IOException; }

    private final Path file;
    private final AtomicReplace replace;
    private List<Entry> entries;
    // Pending records are hidden in this process; a fresh store exposes them as interrupted.
    private final Set<String> pendingIds = new HashSet<>();
    private final Set<String> failedFinalSaves = new HashSet<>();
    private IOException readFailure;

    ScanHistoryStore(File file) {
        this(file, (temporary, destination) -> Files.move(temporary, destination,
                StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING));
    }

    ScanHistoryStore(File file, AtomicReplace replace) {
        this.file = file.toPath();
        this.replace = replace;
    }

    void load() throws IOException {
        if (readFailure != null) throw new IOException("历史文件无法读取，已保留原文件", readFailure);
        if (entries != null) return;
        try {
            BasicFileAttributes attributes;
            try {
                attributes = Files.readAttributes(file, BasicFileAttributes.class, LinkOption.NOFOLLOW_LINKS);
            } catch (NoSuchFileException missing) {
                entries = new ArrayList<>();
                return;
            }
            if (!attributes.isRegularFile() || attributes.size() > MAX_FILE_BYTES)
                throw new IOException("无效历史文件");
            List<Entry> loaded = new ArrayList<>();
            Set<String> ids = new HashSet<>();
            try (DataInputStream input = new DataInputStream(new BufferedInputStream(Files.newInputStream(file)))) {
                if (input.readInt() != MAGIC || input.readInt() != VERSION) throw new IOException("不支持的历史格式");
                int count = count(input.readInt(), LIMIT);
                for (int index = 0; index < count; index++) {
                    Entry entry = readEntry(input);
                    if (!ids.add(entry.id)) throw new IOException("重复历史编号");
                    loaded.add(entry);
                }
                if (input.read() != -1) throw new IOException("历史文件包含额外数据");
            }
            sort(loaded);
            entries = loaded;
        } catch (IOException | IllegalArgumentException e) {
            readFailure = new IOException("历史文件无法读取，已保留原文件", e);
            throw readFailure;
        }
    }

    /** A deliberate read retry may unlock writes, but only after validating the entire file again. */
    boolean retryRead() throws IOException {
        boolean retrying = readFailure != null;
        if (retrying) readFailure = null;
        load();
        return retrying;
    }

    List<Map<String, Object>> summaries() throws IOException {
        load();
        List<Map<String, Object>> values = new ArrayList<>();
        for (Entry entry : entries) if (!pendingIds.contains(entry.id)) values.add(entry.summary());
        return values;
    }

    Map<String, Object> entry(String id) throws IOException {
        requireId(id);
        load();
        for (Entry entry : entries) if (entry.id.equals(id) && !pendingIds.contains(id)) return entry.full();
        throw new FileNotFoundException("这条历史记录已不存在，请刷新列表");
    }

    void save(Entry entry, boolean pending) throws IOException {
        load();
        if (pending && !entry.status.equals("interrupted")) throw new IllegalArgumentException("无效的进行中记录");
        List<Entry> updated = new ArrayList<>();
        updated.add(entry);
        for (Entry previous : entries) if (!previous.id.equals(entry.id)) updated.add(previous);
        sort(updated);
        if (updated.size() > LIMIT) updated = new ArrayList<>(updated.subList(0, LIMIT));
        write(updated);
        entries = updated;
        if (pending) pendingIds.add(entry.id); else pendingIds.remove(entry.id);
        if (!pending) failedFinalSaves.remove(entry.id);
        Set<String> remaining = new HashSet<>();
        for (Entry item : entries) remaining.add(item.id);
        pendingIds.retainAll(remaining);
    }

    /** Expose the last successfully saved checkpoint if its final replacement failed. */
    boolean revealFailedFinalSave(String id) {
        failedFinalSaves.add(id);
        return pendingIds.remove(id);
    }

    boolean hasFailedFinalSaves() { return !failedFinalSaves.isEmpty(); }

    boolean delete(String id) throws IOException {
        requireId(id);
        load();
        if (pendingIds.contains(id)) throw new IllegalArgumentException("正在扫描的记录暂时不能删除");
        List<Entry> updated = new ArrayList<>();
        for (Entry entry : entries) if (!entry.id.equals(id)) updated.add(entry);
        if (updated.size() == entries.size()) return false;
        write(updated);
        entries = updated;
        failedFinalSaves.remove(id);
        return true;
    }

    static final class Entry {
        final String id, status, host;
        final long startedAt, finishedAt;
        final int ipVersion, budget, completed, total;
        final List<Map<String, Object>> results;
        final Map<String, Object> stats;

        private Entry(String id, long startedAt, long finishedAt, String status, int ipVersion, String host,
                int budget, int completed, int total, List<Map<String, Object>> results, Map<String, Object> stats) {
            requireId(id);
            if (startedAt < 0 || finishedAt < startedAt || !TERMINAL.contains(status) || ipVersion != 4 && ipVersion != 6
                    || budget < 1 || budget > 1_000_000 || completed < 0 || completed > 1_000_000 || total < 0 || total > 1_000_000)
                throw new IllegalArgumentException("无效历史摘要");
            this.id = id; this.startedAt = startedAt; this.finishedAt = finishedAt; this.status = status;
            this.ipVersion = ipVersion; this.host = host; this.budget = budget; this.completed = completed; this.total = total;
            this.results = Collections.unmodifiableList(results); this.stats = Collections.unmodifiableMap(stats);
        }

        Map<String, Object> summary() {
            Map<String, Object> value = new LinkedHashMap<>();
            value.put("id", id); value.put("startedAt", startedAt); value.put("finishedAt", finishedAt); value.put("status", status);
            value.put("ipVersion", ipVersion); value.put("host", host); value.put("budget", budget);
            value.put("completed", completed); value.put("total", total); value.put("resultCount", results.size());
            return value;
        }

        Map<String, Object> full() {
            Map<String, Object> value = summary();
            List<Map<String, Object>> rows = new ArrayList<>();
            for (Map<String, Object> row : results) rows.add(copyMap(row));
            value.put("results", rows); value.put("stats", copyMap(stats));
            return value;
        }
    }

    /** Only these fields cross the persistence boundary; no logs, URLs, proxy data or free-form errors. */
    static Entry capture(String id, long startedAt, long finishedAt, String status, int ipVersion, String host,
            int budget, int completed, int total, List<? extends Map<String, Object>> results, Map<String, Object> stats) {
        List<Map<String, Object>> safeResults = new ArrayList<>();
        if (results.size() > MAX_RESULTS) throw new IllegalArgumentException("历史结果数量超出上限");
        for (Map<String, Object> source : results) {
            if (!Boolean.TRUE.equals(source.get("ok"))) continue;
            String ip = text(source.get("ip"), "[0-9A-Fa-f:.]{1,45}");
            if (!isAddress(ip)) continue;
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("ip", ip);
            String prefix = text(source.get("prefix"), "[0-9A-Fa-f:./]{1,49}");
            if (!prefix.isEmpty()) row.put("prefix", prefix);
            for (String key : Arrays.asList("ok", "download_ok")) if (source.get(key) instanceof Boolean) row.put(key, source.get(key));
            for (String key : RESULT_NUMBERS) number(row, source, key, false);
            Object trace = source.get("trace");
            if (trace instanceof Map) {
                String colo = text(((Map<?, ?>) trace).get("colo"), "[A-Za-z0-9-]{1,16}");
                if (!colo.isEmpty()) row.put("trace", Collections.singletonMap("colo", colo));
            }
            Object downloadError = source.get("download_error");
            if (downloadError instanceof String && !((String) downloadError).isEmpty()) row.put("download_error", DOWNLOAD_FAILURE);
            safeResults.add(Collections.unmodifiableMap(row));
        }
        Map<String, Object> safeStats = new LinkedHashMap<>();
        for (String key : STAT_NUMBERS) number(safeStats, stats, key, key.equals("seed"));
        if (stats.get("exhausted") instanceof Boolean) safeStats.put("exhausted", stats.get("exhausted"));
        String safeHost = safeHost(host);
        return new Entry(id, startedAt, Math.max(startedAt, finishedAt), status, ipVersion, safeHost, budget,
                Math.max(0, Math.min(1_000_000, completed)), Math.max(0, Math.min(1_000_000, total)), safeResults, safeStats);
    }

    private static String safeHost(String host) {
        if (host == null) return "";
        String normalized = host.trim();
        if (normalized.indexOf(':') < 0) {
            try { normalized = IDN.toASCII(normalized, IDN.USE_STD3_ASCII_RULES); }
            catch (IllegalArgumentException invalid) { return ""; }
        }
        return text(normalized, "[A-Za-z0-9._:\\[\\]-]{1,253}");
    }

    // Numeric-only parsing avoids any DNS lookup while rejecting malformed addresses.
    private static boolean isAddress(String ip) {
        if (ip.indexOf(':') < 0) return ipv4(ip);
        int compression = ip.indexOf("::");
        if (compression < 0) return ipv6Groups(ip, true) == 8;
        if (ip.indexOf("::", compression + 2) >= 0) return false;
        int left = ipv6Groups(ip.substring(0, compression), false);
        int right = ipv6Groups(ip.substring(compression + 2), true);
        return left >= 0 && right >= 0 && left + right < 8;
    }

    private static boolean ipv4(String ip) {
        String[] parts = ip.split("\\.", -1);
        if (parts.length != 4) return false;
        for (String part : parts) {
            if (!part.matches("[0-9]{1,3}") || part.length() > 1 && part.charAt(0) == '0' || Integer.parseInt(part) > 255) return false;
        }
        return true;
    }

    private static int ipv6Groups(String value, boolean allowIpv4Tail) {
        if (value.isEmpty()) return 0;
        String[] groups = value.split(":", -1);
        int count = 0;
        for (int i = 0; i < groups.length; i++) {
            String group = groups[i];
            if (allowIpv4Tail && i == groups.length - 1 && group.indexOf('.') >= 0) {
                if (!ipv4(group)) return -1;
                count += 2;
            } else {
                if (!group.matches("[0-9A-Fa-f]{1,4}")) return -1;
                count++;
            }
        }
        return count;
    }

    private static String text(Object value, String allowed) {
        if (!(value instanceof String)) return "";
        String text = ((String) value).trim();
        return text.matches(allowed) ? text : "";
    }

    private static void number(Map<String, Object> destination, Map<String, Object> source, String key, boolean signed) {
        Object value = source.get(key);
        if (!(value instanceof Number)) return;
        Number n = (Number) value;
        if (!Double.isFinite(n.doubleValue()) || !signed && n.doubleValue() < 0) return;
        if (value instanceof Float || value instanceof Double) destination.put(key, n.doubleValue());
        else if (value instanceof Byte || value instanceof Short || value instanceof Integer || value instanceof Long) destination.put(key, n.longValue());
    }

    private void write(List<Entry> updated) throws IOException {
        Path parent = file.toAbsolutePath().getParent();
        Files.createDirectories(parent);
        Path temporary = Files.createTempFile(parent, "scan-history-", ".tmp");
        try {
            try (FileOutputStream output = new FileOutputStream(temporary.toFile());
                 DataOutputStream data = new DataOutputStream(new BufferedOutputStream(output))) {
                data.writeInt(MAGIC); data.writeInt(VERSION); data.writeInt(updated.size());
                for (Entry entry : updated) writeEntry(data, entry);
                data.flush();
                output.getFD().sync();
            }
            replace.replace(temporary, file.toAbsolutePath());
        } finally {
            Files.deleteIfExists(temporary);
        }
    }

    private static void sort(List<Entry> values) {
        values.sort(Comparator.comparingLong((Entry entry) -> entry.startedAt).reversed().thenComparing(entry -> entry.id));
    }

    private static void requireId(String id) {
        if (id == null || !id.matches("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"))
            throw new IllegalArgumentException("无效历史记录编号");
    }

    private static void writeEntry(DataOutputStream data, Entry entry) throws IOException {
        data.writeUTF(entry.id); data.writeLong(entry.startedAt); data.writeLong(entry.finishedAt); data.writeUTF(entry.status);
        data.writeInt(entry.ipVersion); data.writeUTF(entry.host); data.writeInt(entry.budget); data.writeInt(entry.completed); data.writeInt(entry.total);
        data.writeInt(entry.results.size());
        for (Map<String, Object> row : entry.results) writeMap(data, row);
        writeMap(data, entry.stats);
    }

    private static Entry readEntry(DataInputStream input) throws IOException {
        String id = input.readUTF(); long startedAt = input.readLong(), finishedAt = input.readLong(); String status = input.readUTF();
        int ipVersion = input.readInt(); String host = input.readUTF(); int budget = input.readInt(), completed = input.readInt(), total = input.readInt();
        int resultCount = count(input.readInt(), MAX_RESULTS);
        List<Map<String, Object>> results = new ArrayList<>();
        for (int i = 0; i < resultCount; i++) results.add(readMap(input, 0));
        Map<String, Object> stats = readMap(input, 0);
        Entry entry = capture(id, startedAt, finishedAt, status, ipVersion, host, budget, completed, total, results, stats);
        if (!entry.host.equals(host) || entry.finishedAt != finishedAt || entry.completed != completed || entry.total != total
                || !entry.results.equals(results) || !entry.stats.equals(stats)) throw new IOException("历史数据不符合格式");
        return entry;
    }

    private static void writeMap(DataOutputStream output, Map<String, Object> values) throws IOException {
        output.writeInt(values.size());
        for (Map.Entry<String, Object> entry : values.entrySet()) {
            output.writeUTF(entry.getKey());
            Object value = entry.getValue();
            if (value instanceof String) { output.writeByte(1); output.writeUTF((String) value); }
            else if (value instanceof Long) { output.writeByte(2); output.writeLong((Long) value); }
            else if (value instanceof Double) { output.writeByte(3); output.writeDouble((Double) value); }
            else if (value instanceof Boolean) { output.writeByte(4); output.writeBoolean((Boolean) value); }
            else if (value instanceof Map) {
                output.writeByte(5);
                @SuppressWarnings("unchecked") Map<String, Object> nested = (Map<String, Object>) value;
                writeMap(output, nested);
            } else throw new IOException("无效历史字段");
        }
    }

    private static Map<String, Object> readMap(DataInputStream input, int depth) throws IOException {
        if (depth > 1) throw new IOException("历史嵌套过深");
        int size = count(input.readInt(), 32);
        Map<String, Object> values = new LinkedHashMap<>();
        for (int i = 0; i < size; i++) {
            String key = input.readUTF(); Object value;
            switch (input.readByte()) {
                case 1: value = input.readUTF(); break;
                case 2: value = input.readLong(); break;
                case 3: value = input.readDouble(); break;
                case 4: value = input.readBoolean(); break;
                case 5: value = readMap(input, depth + 1); break;
                default: throw new IOException("无效历史字段类型");
            }
            if (values.put(key, value) != null) throw new IOException("重复历史字段");
        }
        return values;
    }

    private static int count(int value, int limit) throws IOException {
        if (value < 0 || value > limit) throw new IOException("历史数量超出范围");
        return value;
    }

    private static Map<String, Object> copyMap(Map<String, Object> source) {
        Map<String, Object> copy = new LinkedHashMap<>();
        source.forEach((key, value) -> {
            if (value instanceof Map) {
                @SuppressWarnings("unchecked") Map<String, Object> nested = (Map<String, Object>) value;
                copy.put(key, copyMap(nested));
            } else copy.put(key, value);
        });
        return copy;
    }
}
