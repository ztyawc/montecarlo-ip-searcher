package com.ztyawc.mcis;

import android.content.Context;
import android.content.SharedPreferences;
import android.os.Handler;
import android.os.Looper;
import io.flutter.plugin.common.EventChannel;
import io.flutter.plugin.common.MethodChannel;
import org.json.JSONArray;
import org.json.JSONObject;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
import java.util.function.Consumer;

/** All observable state is confined to the main thread; stream readers post ordered updates. */
final class NativeScanner implements EventChannel.StreamHandler, AutoCloseable {
    private static final Set<String> STRINGS = new HashSet<>(Arrays.asList(
            "host", "path", "cidrs", "budget", "concurrency", "top", "heads", "timeout", "rounds",
            "skip_first", "colo", "download_top", "download_mb", "download_timeout", "download_url",
            "proxy_address", "proxy_username", "proxy_timeout"));
    private static final Set<String> INTS = new HashSet<>(Arrays.asList("ip_version", "colo_mode", "download_mode", "proxy_method", "theme"));
    private static final Set<String> BOOLS = new HashSet<>(Arrays.asList("download_enabled", "proxy_enabled"));
    private final Context context;
    private final Consumer<Boolean> runningChanged;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final ExecutorService workers = Executors.newFixedThreadPool(3);
    private boolean closed;
    // Shared across Activity recreation, so an old Activity's final save precedes the next one's reads.
    private static final ExecutorService HISTORY_IO = Executors.newSingleThreadExecutor();
    private static final Map<String, ScanHistoryStore> HISTORY_STORES = new HashMap<>();
    private ScanHistoryStore historyStore; // confined to HISTORY_IO
    private long historyRevision;
    private enum HistoryErrorKind { READ, SAVE, DELETE }
    private final Map<HistoryErrorKind, String> historyErrors = new EnumMap<>(HistoryErrorKind.class);
    private static final String HISTORY_SAVE_FAILURE = "历史记录保存失败，已保留此前记录。";
    private HistoryRun activeHistory;
    private boolean checkpointScheduled;
    private final Runnable checkpoint = () -> {
        checkpointScheduled = false;
        if (!closed && activeHistory != null) saveHistory(activeHistory, "interrupted", true);
    };
    private EventChannel.EventSink sink;
    private boolean emitScheduled;
    private final Runnable publish = () -> {
        emitScheduled = false;
        if (!closed && sink != null) sink.success(snapshot());
    };
    private volatile RunSession active;
    private long runId;
    private String status = "idle", error = "";
    private int completed, total;
    private final List<Map<String, Object>> results = new ArrayList<>();
    private final ArrayDeque<String> logs = new ArrayDeque<>();
    private final Map<String, Object> stats = new HashMap<>();

    NativeScanner(Context context, Consumer<Boolean> runningChanged) {
        this.context = context.getApplicationContext();
        this.runningChanged = runningChanged;
        // No process survives the activity lifecycle. Remove credentials left by a killed app.
        File[] stale = context.getCacheDir().listFiles((dir, name) -> name.startsWith("private-socks-") && name.endsWith(".json"));
        if (stale != null) for (File file : stale) file.delete();
        HISTORY_IO.execute(() -> {
            try {
                File file = new File(this.context.getFilesDir(), "scan-history-v1.bin");
                historyStore = HISTORY_STORES.computeIfAbsent(file.getAbsolutePath(), ignored -> new ScanHistoryStore(file));
                historyStore.load();
                historyChanged(HistoryErrorKind.READ);
                if (historyStore.hasFailedFinalSaves()) historyFailed(HistoryErrorKind.SAVE, HISTORY_SAVE_FAILURE);
            } catch (Exception failure) {
                historyFailed(HistoryErrorKind.READ, "无法读取历史记录，已保留原文件。仍可正常扫描。");
            }
        });
    }

    Map<String, Object> settings() {
        Map<String, Object> values = new HashMap<>();
        context.getSharedPreferences("mcis_settings", Context.MODE_PRIVATE).getAll().forEach((key, value) -> {
            if (STRINGS.contains(key) && value instanceof String || INTS.contains(key) && value instanceof Integer
                    || BOOLS.contains(key) && value instanceof Boolean) values.put(key, value);
        });
        return values;
    }

    void saveSettings(Map<?, ?> values) {
        SharedPreferences.Editor editor = context.getSharedPreferences("mcis_settings", Context.MODE_PRIVATE).edit();
        values.forEach((key, value) -> {
            if (STRINGS.contains(key) && value instanceof String) editor.putString((String) key, (String) value);
            else if (INTS.contains(key) && value instanceof Integer) editor.putInt((String) key, (Integer) value);
            else if (BOOLS.contains(key) && value instanceof Boolean) editor.putBoolean((String) key, (Boolean) value);
        });
        editor.remove("proxy_password").apply();
    }

    Map<String, Object> snapshot() {
        Map<String, Object> value = new HashMap<>();
        value.put("runId", runId); value.put("status", status); value.put("error", error);
        value.put("completed", completed); value.put("total", total);
        value.put("results", new ArrayList<>(results)); value.put("logs", new ArrayList<>(logs));
        value.put("stats", new HashMap<>(stats));
        value.put("historyRevision", historyRevision); value.put("historyError", String.join("\n", historyErrors.values()));
        return value;
    }

    void history(MethodChannel.Result reply) {
        historyCall(reply, HistoryErrorKind.READ, "无法读取历史记录，已保留原文件。仍可正常扫描。", () -> {
            if (historyStore.retryRead()) historyChanged(HistoryErrorKind.READ);
            Map<String, Object> value = new HashMap<>();
            value.put("entries", historyStore.summaries()); value.put("limit", ScanHistoryStore.LIMIT);
            return value;
        });
    }

    void historyEntry(Map<?, ?> arguments, MethodChannel.Result reply) {
        final String id = historyId(arguments);
        historyCall(reply, HistoryErrorKind.READ, "无法读取这条历史记录，已保留原文件。", () -> historyStore.entry(id));
    }

    void deleteHistory(Map<?, ?> arguments, MethodChannel.Result reply) {
        final String id = historyId(arguments);
        historyCall(reply, HistoryErrorKind.DELETE, "删除历史记录失败，原记录已保留。", () -> {
            boolean hadFailedFinalSave = historyStore.hasFailedFinalSaves();
            if (historyStore.delete(id)) {
                historyChanged(HistoryErrorKind.DELETE);
                if (hadFailedFinalSave && !historyStore.hasFailedFinalSaves()) clearHistoryError(HistoryErrorKind.SAVE);
            }
            return null;
        });
    }

    private static String historyId(Map<?, ?> arguments) {
        Object id = arguments == null ? null : arguments.get("id");
        if (!(id instanceof String)) throw new IllegalArgumentException("缺少历史记录编号");
        return (String) id;
    }

    private interface HistoryCall { Object run() throws Exception; }

    private void historyCall(MethodChannel.Result reply, HistoryErrorKind kind, String message, HistoryCall call) {
        HISTORY_IO.execute(() -> {
            try {
                if (historyStore == null) throw new IOException("历史存储未初始化");
                Object value = call.run();
                clearHistoryError(kind);
                main.post(() -> reply.success(value));
            } catch (IllegalArgumentException | FileNotFoundException failure) {
                main.post(() -> reply.error("history", failure.getMessage(), null));
            } catch (Exception failure) {
                historyFailed(kind, message);
                main.post(() -> reply.error("history", message, null));
            }
        });
    }

    private void historyChanged(HistoryErrorKind resolvedError) {
        main.post(() -> {
            if (closed) return;
            historyRevision++;
            if (resolvedError != null) historyErrors.remove(resolvedError);
            emit();
        });
    }

    private void clearHistoryError(HistoryErrorKind kind) {
        main.post(() -> { if (!closed && historyErrors.remove(kind) != null) emit(); });
    }

    private void historyFailed(HistoryErrorKind kind, String message) {
        main.post(() -> { if (!closed) { historyErrors.put(kind, message); emit(); } });
    }

    private void saveHistory(HistoryRun run, String finalStatus, boolean pending) {
        // Rows are immutable after parsing. Copy containers now; sanitize and write on the IO executor.
        List<Map<String, Object>> rows = new ArrayList<>(results);
        Map<String, Object> summary = new HashMap<>(stats);
        int done = completed, count = total;
        long finishedAt = System.currentTimeMillis();
        HISTORY_IO.execute(() -> {
            try {
                if (historyStore == null) throw new IOException("历史存储未初始化");
                ScanHistoryStore.Entry entry = ScanHistoryStore.capture(run.id, run.startedAt, finishedAt,
                        finalStatus, run.ipVersion, run.host, run.budget, done, count, rows, summary);
                historyStore.save(entry, pending);
                historyChanged(historyStore.hasFailedFinalSaves() ? null : HistoryErrorKind.SAVE);
            } catch (Exception failure) {
                if (!pending && historyStore != null && historyStore.revealFailedFinalSave(run.id)) historyChanged(null);
                historyFailed(HistoryErrorKind.SAVE, HISTORY_SAVE_FAILURE);
            }
        });
    }

    private void scheduleHistoryCheckpoint() {
        if (!checkpointScheduled && activeHistory != null) {
            checkpointScheduled = true;
            main.postDelayed(checkpoint, 2000);
        }
    }

    private void cancelHistoryCheckpoint() {
        main.removeCallbacks(checkpoint);
        checkpointScheduled = false;
    }

    private static final class HistoryRun {
        final String id = UUID.randomUUID().toString();
        final long startedAt = System.currentTimeMillis();
        final int ipVersion, budget;
        final String host;
        HistoryRun(Config config) { ipVersion = config.ipv6 ? 6 : 4; budget = config.budget; host = config.s("host"); }
    }

    @Override public void onListen(Object arguments, EventChannel.EventSink events) { sink = events; emit(); }
    @Override public void onCancel(Object arguments) { sink = null; }
    private void emit() {
        // Batch rapid JSONL results: avoid serializing a growing Top-1000 list for every row.
        if (!closed && sink != null && !emitScheduled) {
            emitScheduled = true;
            main.postDelayed(publish, 80);
        }
    }

    void start(Map<?, ?> arguments) {
        if (closed) throw new IllegalStateException("扫描器已关闭");
        if (active != null) throw new IllegalStateException("请等待当前扫描结束");
        Config config = new Config(arguments);
        saveSettings(arguments);
        RunSession session = new RunSession();
        active = session; runId++; status = "preparing"; error = "";
        completed = 0; total = config.budget; results.clear(); logs.clear(); stats.clear();
        activeHistory = new HistoryRun(config);
        saveHistory(activeHistory, "interrupted", true);
        runningChanged.accept(true); emit();
        workers.execute(() -> run(session, config));
    }

    void stop() {
        RunSession session = active;
        if (session == null || session.isStopRequested()) return;
        status = "stopping"; emit();
        session.requestStop();
        main.postDelayed(session.forceStopAction, 2000);
    }

    private void post(RunSession session, Runnable update) {
        main.post(() -> { if (!closed && active == session) { update.run(); emit(); } });
    }

    private void run(RunSession session, Config config) {
        File proxy = null;
        Process child = null;
        Future<?> stdout = null, stderr = null;
        int code = -1;
        String failure = "";
        try {
            if (session.isStopRequested()) return;
            File binary = new File(context.getApplicationInfo().nativeLibraryDir, "libmcis.so");
            if (!binary.isFile() || !binary.canExecute()) throw new IOException("内置扫描核心不可执行");
            File cidrs = new File(context.getFilesDir(), config.ipv6 ? "scan-ipv6.txt" : "scan-ipv4.txt");
            if (config.s("cidrs").isEmpty()) {
                try (InputStream input = context.getAssets().open(config.ipv6 ? "ipv6cidr.txt" : "ipv4cidr.txt");
                     OutputStream output = new FileOutputStream(cidrs)) {
                    byte[] buffer = new byte[8192]; int count;
                    while ((count = input.read(buffer)) != -1) output.write(buffer, 0, count);
                }
            } else write(cidrs, config.s("cidrs") + "\n");
            List<String> command = config.command(binary, cidrs);
            if (config.b("proxy_enabled")) {
                proxy = File.createTempFile("private-socks-", ".json", context.getCacheDir());
                JSONObject json = new JSONObject();
                json.put("address", config.s("proxy_address")); json.put("username", config.s("proxy_username"));
                json.put("password", config.password); json.put("method", config.index("proxy_method", 1) == 0 ? "0x80" : "0x82");
                json.put("handshake_timeout", config.duration("proxy_timeout"));
                write(proxy, json.toString()); arg(command, "--private-socks-config", proxy.getAbsolutePath());
            }
            child = new ProcessBuilder(command).directory(context.getFilesDir()).start();
            if (!session.attach(child)) return;
            post(session, () -> { if (!session.isStopRequested()) status = "scanning"; });
            final Process process = child;
            stdout = workers.submit(() -> read(session, process.getInputStream(), false, config.password));
            stderr = workers.submit(() -> read(session, process.getErrorStream(), true, config.password));
            code = child.waitFor();
            stdout.get(); stderr.get();
        } catch (Exception e) {
            failure = redact(e.getMessage() == null ? e.getClass().getSimpleName() : e.getMessage(), config.password);
            if (e instanceof InterruptedException) Thread.currentThread().interrupt();
        } finally {
            session.close();
            if (child != null) {
                closeStream(child.getInputStream()); closeStream(child.getErrorStream()); closeStream(child.getOutputStream());
            }
            if (stdout != null) stdout.cancel(true);
            if (stderr != null) stderr.cancel(true);
            if (proxy != null) proxy.delete();
            final int exitCode = code; final String problem = failure;
            main.post(() -> {
                main.removeCallbacks(session.forceStopAction);
                if (closed || active != session) return;
                active = null; runningChanged.accept(false);
                status = session.isStopRequested() ? "stopped" : exitCode == 0 && problem.isEmpty() ? "completed" : "error";
                if (status.equals("error")) error = problem.isEmpty() ? "扫描失败（退出码 " + exitCode + "），请查看日志" : problem;
                cancelHistoryCheckpoint();
                if (activeHistory != null) saveHistory(activeHistory, status, false);
                activeHistory = null;
                emit();
            });
        }
    }

    private void read(RunSession session, InputStream stream, boolean diagnostic, String password) {
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(stream, StandardCharsets.UTF_8))) {
            String line;
            while ((line = reader.readLine()) != null) {
                if (diagnostic) {
                    final ScanDiagnostics.Update update = ScanDiagnostics.parse(line, password);
                    post(session, () -> {
                        if (logs.size() == 200) logs.removeFirst();
                        logs.addLast(update.log);
                        if (update.completed != null) completed = update.completed;
                        if (update.total != null) total = update.total;
                        if (update.downloading && !session.isStopRequested()) status = "downloading";
                        stats.putAll(update.stats);
                    });
                } else {
                    Map<String, Object> value = jsonMap(new JSONObject(line));
                    if (Boolean.TRUE.equals(value.get("ok"))) post(session, () -> { results.add(value); scheduleHistoryCheckpoint(); });
                }
            }
        } catch (Exception e) {
            if (!session.isStopRequested()) throw new RuntimeException("读取核心输出失败：" + redact(e.getMessage(), password), e);
        }
    }

    @Override public void close() {
        cancelHistoryCheckpoint();
        if (activeHistory != null) saveHistory(activeHistory, "interrupted", false);
        activeHistory = null;
        closed = true; sink = null;
        main.removeCallbacks(publish); emitScheduled = false;
        if (active != null) { main.removeCallbacks(active.forceStopAction); active.forceStop(); active.close(); active = null; }
        workers.shutdownNow(); runningChanged.accept(false);
        // HISTORY_IO remains alive to flush this Activity's final snapshot after its destruction.
    }
    private static void closeStream(Closeable stream) { try { stream.close(); } catch (IOException ignored) {} }
    private static String redact(String line, String password) { return ScanDiagnostics.redact(line, password); }
    private static void write(File file, String value) throws IOException {
        try (FileOutputStream output = new FileOutputStream(file)) { output.write(value.getBytes(StandardCharsets.UTF_8)); }
    }
    private static void arg(List<String> args, String name, String value) { args.add(name); args.add(value); }
    private static Map<String, Object> jsonMap(JSONObject json) throws Exception {
        Map<String, Object> map = new HashMap<>();
        Iterator<String> keys = json.keys();
        while (keys.hasNext()) { String key = keys.next(); map.put(key, plain(json.get(key))); }
        return map;
    }
    private static Object plain(Object value) throws Exception {
        if (value == JSONObject.NULL) return null;
        if (value instanceof JSONObject) return jsonMap((JSONObject) value);
        if (value instanceof JSONArray) { List<Object> list = new ArrayList<>(); for (int i = 0; i < ((JSONArray) value).length(); i++) list.add(plain(((JSONArray) value).get(i))); return list; }
        return value;
    }

    private static final class Config {
        final Map<?, ?> values;
        final String password;
        final boolean ipv6;
        final int budget;
        Config(Map<?, ?> values) {
            if (values == null) throw new IllegalArgumentException("缺少扫描参数");
            this.values = new HashMap<>(values);
            Object secret = this.values.remove("proxy_password");
            password = b("proxy_enabled") && secret instanceof String ? (String) secret : "";
            ipv6 = index("ip_version", 1) == 1;
            budget = integer("budget", 1, 1_000_000);
            integer("concurrency", 1, 2000); integer("top", 1, Math.min(budget, 1000)); integer("heads", 1, 64);
            integer("skip_first", 0, integer("rounds", 1, 100) - 1); number("timeout", .1, 300);
            if (s("host").isEmpty() || !s("path").startsWith("/")) throw new IllegalArgumentException("请填写域名和以 / 开头的路径");
            if (s("cidrs").length() > 1_000_000) throw new IllegalArgumentException("网段列表过大");
            if (index("colo_mode", 2) != 0 && s("colo").isEmpty()) throw new IllegalArgumentException("请填写机房代码");
            if (b("download_enabled")) {
                integer("download_top", 1, 100); integer("download_mb", 1, 10000); number("download_timeout", 1, 3600); index("download_mode", 1);
                if (!s("download_url").isEmpty() && !s("download_url").startsWith("https://")) throw new IllegalArgumentException("测速地址必须使用 HTTPS");
            }
            if (b("proxy_enabled")) {
                if (s("proxy_address").isEmpty() || s("proxy_username").getBytes(StandardCharsets.UTF_8).length != 19 || password.isEmpty())
                    throw new IllegalArgumentException("请填写代理地址、19 字节用户名和密码");
                number("proxy_timeout", .1, 300); index("proxy_method", 1);
            }
        }
        String s(String key) { Object value = values.get(key); return value instanceof String ? ((String) value).trim() : ""; }
        boolean b(String key) { return Boolean.TRUE.equals(values.get(key)); }
        int index(String key, int max) { Object value = values.get(key); int n = value instanceof Integer ? (Integer) value : -1; if (n < 0 || n > max) throw new IllegalArgumentException("无效选项：" + key); return n; }
        double number(String key, double min, double max) {
            double n; try { n = Double.parseDouble(s(key)); } catch (NumberFormatException e) { throw new IllegalArgumentException("无效数值：" + key); }
            if (!Double.isFinite(n) || n < min || n > max) throw new IllegalArgumentException("数值超出范围：" + key); return n;
        }
        int integer(String key, int min, int max) { number(key, min, max); try { return Integer.parseInt(s(key)); } catch (NumberFormatException e) { throw new IllegalArgumentException("必须为整数：" + key); } }
        String duration(String key) { return new java.math.BigDecimal(s(key)).stripTrailingZeros().toPlainString() + "s"; }
        List<String> command(File binary, File cidrs) {
            List<String> args = new ArrayList<>(); args.add(binary.getAbsolutePath());
            arg(args, "--cidr-file", cidrs.getAbsolutePath());
            for (String key : Arrays.asList("host", "path", "budget", "concurrency", "heads", "top", "rounds", "skip_first")) arg(args, "--" + key.replace('_', '-'), s(key));
            arg(args, "--timeout", duration("timeout")); arg(args, "--out", "jsonl"); args.add("-v");
            int colo = index("colo_mode", 2);
            if (colo != 0) arg(args, colo == 1 ? "--colo" : "--colo-exclude", s("colo").toUpperCase(Locale.ROOT));
            arg(args, "--download-top", b("download_enabled") ? s("download_top") : "0");
            if (b("download_enabled")) {
                arg(args, "--download-bytes", Long.toString(integer("download_mb", 1, 10000) * 1_000_000L));
                arg(args, "--download-timeout", duration("download_timeout"));
                arg(args, "--download-mode", index("download_mode", 1) == 1 ? "sequential" : "all");
                if (!s("download_url").isEmpty()) arg(args, "--download-url", s("download_url"));
            }
            return args;
        }
    }
}
