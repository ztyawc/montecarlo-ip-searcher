package com.ztyawc.mcis;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.SharedPreferences;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.Insets;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.os.Build;
import android.os.Bundle;
import android.text.InputType;
import android.view.Gravity;
import android.view.HapticFeedbackConstants;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowInsets;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.Switch;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class MainActivity extends Activity {
    private static final int COLOR_DARK = Color.rgb(28, 28, 30);
    private static final int COLOR_BLUE = Color.rgb(0, 122, 255);
    private static final int COLOR_GREEN = Color.rgb(52, 199, 89);
    private static final int COLOR_RED = Color.rgb(255, 59, 48);
    private static final int COLOR_BG = Color.rgb(242, 242, 247);
    private static final int COLOR_FIELD = Color.rgb(246, 246, 248);
    private static final int COLOR_MUTED = Color.rgb(99, 99, 102);
    private static final int COLOR_BORDER = Color.rgb(229, 229, 234);
    private static final Pattern PROGRESS_PATTERN = Pattern.compile("progress:\\s+(\\d+)/(\\d+)");

    private final ExecutorService workerPool = Executors.newFixedThreadPool(3);
    private final Object resultLock = new Object();
    private final Object logLock = new Object();
    private final ScanResults results = new ScanResults();
    private boolean resultUpdateScheduled;
    private int resultPage;
    private final StringBuilder logBuffer = new StringBuilder();

    private SharedPreferences preferences;
    private volatile RunSession activeRun;
    private volatile boolean destroyed;
    private volatile boolean running;

    private SegmentedControl ipVersionSpinner;
    private EditText hostField;
    private EditText pathField;
    private EditText cidrField;
    private EditText budgetField;
    private EditText concurrencyField;
    private EditText headsField;
    private EditText topField;
    private EditText timeoutField;
    private EditText roundsField;
    private EditText skipFirstField;
    private SegmentedControl coloModeSpinner;
    private EditText coloField;

    private Switch downloadSwitch;
    private LinearLayout downloadOptions;
    private EditText downloadTopField;
    private EditText downloadMbField;
    private EditText downloadTimeoutField;
    private EditText downloadUrlField;
    private SegmentedControl downloadModeSpinner;

    private Switch proxySwitch;
    private LinearLayout proxyOptions;
    private EditText proxyAddressField;
    private EditText proxyUsernameField;
    private EditText proxyPasswordField;
    private SegmentedControl proxyMethodSpinner;
    private EditText proxyTimeoutField;

    private ProgressBar progressBar;
    private TextView statusText;
    private TextView resultText;
    private LinearLayout resultPager;
    private TextView resultPageText;
    private Button previousResultsButton;
    private Button nextResultsButton;
    private TextView logText;
    private LinearLayout logContainer;
    private Button logToggleButton;
    private boolean logVisible;
    private ScrollView mainScrollView;
    private View statusCard;
    private Button startButton;
    private Button stopButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        preferences = getSharedPreferences("mcis_settings", MODE_PRIVATE);
        getWindow().setStatusBarColor(COLOR_BG);
        getWindow().setNavigationBarColor(COLOR_BG);
        int systemUi = View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            systemUi |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
        }
        getWindow().getDecorView().setSystemUiVisibility(systemUi);
        buildInterface();
    }

    private void buildInterface() {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(COLOR_BG);
        root.setPadding(dp(18), dp(10), dp(18), dp(10));

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            root.setOnApplyWindowInsetsListener((view, windowInsets) -> {
                Insets bars = windowInsets.getInsets(WindowInsets.Type.systemBars());
                view.setPadding(dp(18) + bars.left, dp(10) + bars.top,
                        dp(18) + bars.right, dp(10) + bars.bottom);
                return windowInsets;
            });
        } else {
            root.setOnApplyWindowInsetsListener((view, windowInsets) -> {
                view.setPadding(dp(18) + windowInsets.getSystemWindowInsetLeft(),
                        dp(10) + windowInsets.getSystemWindowInsetTop(),
                        dp(18) + windowInsets.getSystemWindowInsetRight(),
                        dp(10) + windowInsets.getSystemWindowInsetBottom());
                return windowInsets;
            });
        }

        root.addView(buildNavigationHeader(), matchWrap());

        mainScrollView = new ScrollView(this);
        mainScrollView.setFillViewport(true);
        mainScrollView.setVerticalScrollBarEnabled(false);
        mainScrollView.setOverScrollMode(View.OVER_SCROLL_NEVER);
        LinearLayout content = new LinearLayout(this);
        content.setOrientation(LinearLayout.VERTICAL);
        mainScrollView.addView(content, matchWrap());

        buildHeroCard(content);
        buildScanCard(content);
        buildStatusCard(content);
        buildAdvancedCard(content);
        buildDownloadCard(content);
        buildProxyCard(content);
        buildNotesCard(content);

        LinearLayout.LayoutParams scrollParams = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f);
        root.addView(mainScrollView, scrollParams);
        root.addView(buildActionBar(), matchWrap());
        setContentView(root);
    }

    private LinearLayout buildNavigationHeader() {
        LinearLayout header = new LinearLayout(this);
        header.setOrientation(LinearLayout.HORIZONTAL);
        header.setGravity(Gravity.CENTER_VERTICAL);
        header.setPadding(0, dp(2), 0, dp(14));

        TextView icon = text("M", 21, Color.WHITE, Typeface.BOLD);
        icon.setGravity(Gravity.CENTER);
        icon.setBackground(rounded(COLOR_BLUE, 13, COLOR_BLUE));
        LinearLayout.LayoutParams iconParams = new LinearLayout.LayoutParams(dp(46), dp(46));
        iconParams.rightMargin = dp(12);
        header.addView(icon, iconParams);

        LinearLayout labels = new LinearLayout(this);
        labels.setOrientation(LinearLayout.VERTICAL);
        TextView title = text("IP 优选", 30, COLOR_DARK, Typeface.BOLD);
        title.setLetterSpacing(-0.02f);
        labels.addView(title, matchWrap());
        labels.addView(text("MCIS · 智能边缘节点搜索", 12, COLOR_MUTED, Typeface.NORMAL), matchWrap());
        header.addView(labels, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f));

        TextView badge = text("v2", 12, COLOR_BLUE, Typeface.BOLD);
        badge.setGravity(Gravity.CENTER);
        badge.setBackground(rounded(Color.rgb(232, 242, 255), 12, Color.rgb(232, 242, 255)));
        header.addView(badge, new LinearLayout.LayoutParams(dp(42), dp(28)));
        return header;
    }

    private void buildHeroCard(LinearLayout content) {
        LinearLayout hero = new LinearLayout(this);
        hero.setOrientation(LinearLayout.VERTICAL);
        hero.setPadding(dp(18), dp(18), dp(18), dp(18));
        GradientDrawable background = new GradientDrawable(
                GradientDrawable.Orientation.TL_BR,
                new int[]{Color.rgb(0, 122, 255), Color.rgb(88, 86, 214)});
        background.setCornerRadius(dp(20));
        hero.setBackground(background);
        hero.setElevation(dp(3));

        TextView eyebrow = text("MONTE CARLO IP SEARCH", 11,
                Color.argb(205, 255, 255, 255), Typeface.BOLD);
        eyebrow.setLetterSpacing(0.08f);
        hero.addView(eyebrow, matchWrap());
        LinearLayout.LayoutParams headlineParams = matchWrap();
        headlineParams.topMargin = dp(6);
        TextView headline = text("找到更快的边缘节点", 23, Color.WHITE, Typeface.BOLD);
        hero.addView(headline, headlineParams);
        TextView copy = text("多头采样、实时排序，所有计算都在本机完成。", 13,
                Color.argb(220, 255, 255, 255), Typeface.NORMAL);
        LinearLayout.LayoutParams copyParams = matchWrap();
        copyParams.topMargin = dp(5);
        hero.addView(copy, copyParams);

        LinearLayout chips = new LinearLayout(this);
        chips.setOrientation(LinearLayout.HORIZONTAL);
        chips.setPadding(0, dp(14), 0, 0);
        chips.addView(heroChip("IPv4 / IPv6"), wrapWrapWithEndMargin(8));
        chips.addView(heroChip("ARM64"), wrapWrapWithEndMargin(8));
        chips.addView(heroChip("本地运行"), new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, dp(28)));
        hero.addView(chips, matchWrap());
        addCard(content, hero);
    }

    private TextView heroChip(String value) {
        TextView chip = text(value, 11, Color.WHITE, Typeface.BOLD);
        chip.setGravity(Gravity.CENTER);
        chip.setPadding(dp(10), 0, dp(10), 0);
        chip.setBackground(rounded(Color.argb(42, 255, 255, 255), 12,
                Color.argb(72, 255, 255, 255)));
        return chip;
    }

    private void buildScanCard(LinearLayout content) {
        LinearLayout card = card("扫描设置");
        addLabel(card, "IP 协议");
        ipVersionSpinner = spinner(new String[]{"IPv4", "IPv6"}, preferences.getInt("ip_version", 0));
        card.addView(ipVersionSpinner, matchWrap());

        hostField = input(preferences.getString("host", "www.cloudflare.com"),
                "用于 TLS SNI 与 HTTP Host 的域名", InputType.TYPE_CLASS_TEXT, true);
        addLabeled(card, "目标域名", hostField);

        pathField = input(preferences.getString("path", "/cdn-cgi/trace"),
                "/cdn-cgi/trace", InputType.TYPE_CLASS_TEXT, true);
        addLabeled(card, "请求路径", pathField);

        cidrField = input(preferences.getString("cidrs", ""),
                "留空使用内置网段；自定义时每行一个 CIDR", InputType.TYPE_CLASS_TEXT
                        | InputType.TYPE_TEXT_FLAG_MULTI_LINE, false);
        cidrField.setMinLines(2);
        cidrField.setMaxLines(6);
        cidrField.setGravity(Gravity.TOP | Gravity.START);
        addLabeled(card, "自定义 CIDR（可选）", cidrField);
        addCard(content, card);
    }

    private void buildAdvancedCard(LinearLayout content) {
        LinearLayout card = card("算法参数");
        budgetField = numeric(preferences.getString("budget", "2000"), "2000");
        concurrencyField = numeric(preferences.getString("concurrency", "200"), "200");
        addPair(card, "探测预算", budgetField, "并发数", concurrencyField);

        topField = numeric(preferences.getString("top", "20"), "20");
        headsField = numeric(preferences.getString("heads", "4"), "4");
        addPair(card, "结果数量", topField, "搜索头数", headsField);

        timeoutField = decimal(preferences.getString("timeout", "3"), "3");
        roundsField = numeric(preferences.getString("rounds", "6"), "6");
        addPair(card, "单轮超时（秒）", timeoutField, "每 IP 轮数", roundsField);

        skipFirstField = numeric(preferences.getString("skip_first", "1"), "1");
        addLabeled(card, "跳过前 N 轮", skipFirstField);

        addLabel(card, "节点筛选");
        coloModeSpinner = spinner(new String[]{"不筛选", "仅允许这些 Colo", "排除这些 Colo"},
                preferences.getInt("colo_mode", 0));
        card.addView(coloModeSpinner, matchWrap());
        coloField = input(preferences.getString("colo", ""), "例如 HKG,SJC,NRT",
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_CAP_CHARACTERS, true);
        addLabeled(card, "Colo 代码（逗号分隔）", coloField);
        addCard(content, card);
    }

    private void buildDownloadCard(LinearLayout content) {
        LinearLayout card = card("下载测速");
        downloadSwitch = iosSwitch(preferences.getBoolean("download_enabled", false));
        card.addView(toggleRow("下载测速", "扫描完成后测试入选 IP 的实际速度", downloadSwitch), matchWrap());

        downloadOptions = new LinearLayout(this);
        downloadOptions.setOrientation(LinearLayout.VERTICAL);
        downloadOptions.setPadding(0, dp(8), 0, 0);
        downloadTopField = numeric(preferences.getString("download_top", "3"), "3");
        downloadMbField = numeric(preferences.getString("download_mb", "10"), "10");
        addPair(downloadOptions, "测速 IP 数", downloadTopField, "每个 IP 上限（MB）", downloadMbField);

        downloadTimeoutField = decimal(preferences.getString("download_timeout", "45"), "45");
        addLabeled(downloadOptions, "单 IP 测速超时（秒）", downloadTimeoutField);
        downloadUrlField = input(preferences.getString("download_url", ""),
                "留空使用 speed.cloudflare.com", InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI, true);
        addLabeled(downloadOptions, "自定义 HTTPS 下载 URL（可选）", downloadUrlField);
        addLabel(downloadOptions, "失败处理");
        downloadModeSpinner = spinner(new String[]{"固定测试前 N 个", "顺序测试直到 N 个成功"},
                preferences.getInt("download_mode", 0));
        downloadOptions.addView(downloadModeSpinner, matchWrap());
        card.addView(downloadOptions, matchWrap());

        downloadOptions.setVisibility(downloadSwitch.isChecked() ? View.VISIBLE : View.GONE);
        downloadSwitch.setOnCheckedChangeListener((button, checked) ->
                toggleSection(downloadOptions, checked));
        addCard(content, card);
    }

    private void buildProxyCard(LinearLayout content) {
        LinearLayout card = card("私有 SOCKS");
        proxySwitch = iosSwitch(preferences.getBoolean("proxy_enabled", false));
        card.addView(toggleRow("启用代理", "支持私有认证方法 0x80 / 0x82", proxySwitch), matchWrap());

        proxyOptions = new LinearLayout(this);
        proxyOptions.setOrientation(LinearLayout.VERTICAL);
        proxyOptions.setPadding(0, dp(8), 0, 0);
        proxyAddressField = input(preferences.getString("proxy_address", ""), "服务器:端口",
                InputType.TYPE_CLASS_TEXT, true);
        addLabeled(proxyOptions, "代理地址", proxyAddressField);
        proxyUsernameField = input(preferences.getString("proxy_username", ""), "用户名",
                InputType.TYPE_CLASS_TEXT, true);
        addLabeled(proxyOptions, "用户名", proxyUsernameField);
        proxyPasswordField = input("", "本次运行使用，不会保存",
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD, true);
        addLabeled(proxyOptions, "密码", proxyPasswordField);
        addLabel(proxyOptions, "认证方法");
        proxyMethodSpinner = spinner(new String[]{"0x80", "0x82"}, preferences.getInt("proxy_method", 0));
        proxyOptions.addView(proxyMethodSpinner, matchWrap());
        proxyTimeoutField = decimal(preferences.getString("proxy_timeout", "10"), "10");
        addLabeled(proxyOptions, "握手超时（秒）", proxyTimeoutField);
        card.addView(proxyOptions, matchWrap());

        proxyOptions.setVisibility(proxySwitch.isChecked() ? View.VISIBLE : View.GONE);
        proxySwitch.setOnCheckedChangeListener((button, checked) ->
                toggleSection(proxyOptions, checked));
        addCard(content, card);
    }

    private void buildStatusCard(LinearLayout content) {
        LinearLayout card = card("运行状态");
        statusCard = card;
        LinearLayout statusRow = new LinearLayout(this);
        statusRow.setOrientation(LinearLayout.HORIZONTAL);
        statusRow.setGravity(Gravity.CENTER_VERTICAL);
        statusRow.setPadding(dp(12), dp(10), dp(12), dp(10));
        statusRow.setBackground(rounded(Color.rgb(237, 248, 240), 11,
                Color.rgb(237, 248, 240)));
        TextView statusDot = text("●", 12, COLOR_GREEN, Typeface.BOLD);
        LinearLayout.LayoutParams dotParams = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        dotParams.rightMargin = dp(8);
        statusRow.addView(statusDot, dotParams);
        statusText = text("准备就绪", 15, COLOR_DARK, Typeface.BOLD);
        statusRow.addView(statusText, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        TextView localBadge = text("本机", 11, COLOR_GREEN, Typeface.BOLD);
        localBadge.setGravity(Gravity.CENTER);
        statusRow.addView(localBadge, new LinearLayout.LayoutParams(dp(38), dp(24)));
        card.addView(statusRow, matchWrap());

        progressBar = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        progressBar.setMax(100);
        progressBar.setProgress(0);
        progressBar.setProgressDrawable(getDrawable(R.drawable.progress_ios));
        LinearLayout.LayoutParams progressParams = matchWrap();
        progressParams.height = dp(7);
        progressParams.topMargin = dp(12);
        progressParams.bottomMargin = dp(16);
        card.addView(progressBar, progressParams);

        TextView resultLabel = text("优选结果", 13, COLOR_MUTED, Typeface.BOLD);
        card.addView(resultLabel, matchWrap());
        resultText = text("运行完成后，结果会显示在这里。", 12,
                Color.rgb(235, 235, 245), Typeface.NORMAL);
        resultText.setTypeface(Typeface.MONOSPACE);
        resultText.setTextIsSelectable(true);
        resultText.setMinHeight(dp(104));
        resultText.setGravity(Gravity.TOP | Gravity.START);
        resultText.setPadding(dp(13), dp(13), dp(13), dp(13));
        resultText.setBackground(rounded(Color.rgb(28, 28, 30), 12, Color.rgb(28, 28, 30)));
        LinearLayout.LayoutParams outputParams = matchWrap();
        outputParams.topMargin = dp(7);
        outputParams.bottomMargin = dp(10);
        card.addView(resultText, outputParams);

        resultPager = new LinearLayout(this);
        resultPager.setOrientation(LinearLayout.HORIZONTAL);
        resultPager.setGravity(Gravity.CENTER_VERTICAL);
        previousResultsButton = secondaryButton("上一页");
        previousResultsButton.setOnClickListener(view -> showResultPage(-1));
        nextResultsButton = secondaryButton("下一页");
        nextResultsButton.setOnClickListener(view -> showResultPage(1));
        resultPageText = text("", 12, COLOR_MUTED, Typeface.NORMAL);
        resultPageText.setGravity(Gravity.CENTER);
        resultPager.addView(previousResultsButton, weightedButtonParams(1f, 0));
        resultPager.addView(resultPageText, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        resultPager.addView(nextResultsButton, weightedButtonParams(1f, 0));
        resultPager.setVisibility(View.GONE);
        card.addView(resultPager, matchWrap());

        LinearLayout outputButtons = new LinearLayout(this);
        outputButtons.setOrientation(LinearLayout.HORIZONTAL);
        Button copyButton = secondaryButton("复制全部");
        copyButton.setOnClickListener(view -> copyResults());
        Button clearButton = secondaryButton("清空输出");
        clearButton.setOnClickListener(view -> clearOutput());
        outputButtons.addView(copyButton, weightedButtonParams(1f, 8));
        outputButtons.addView(clearButton, weightedButtonParams(1f, 0));
        card.addView(outputButtons, matchWrap());

        logToggleButton = disclosureButton("显示详细日志  ›");
        LinearLayout.LayoutParams logButtonParams = matchWrap();
        logButtonParams.topMargin = dp(8);
        card.addView(logToggleButton, logButtonParams);

        logContainer = new LinearLayout(this);
        logContainer.setOrientation(LinearLayout.VERTICAL);
        logContainer.setVisibility(View.GONE);
        TextView logLabel = text("详细日志", 12, COLOR_MUTED, Typeface.BOLD);
        LinearLayout.LayoutParams logLabelParams = matchWrap();
        logLabelParams.topMargin = dp(12);
        logLabelParams.bottomMargin = dp(6);
        logContainer.addView(logLabel, logLabelParams);
        logText = text("", 11, COLOR_MUTED, Typeface.NORMAL);
        logText.setTypeface(Typeface.MONOSPACE);
        logText.setTextIsSelectable(true);
        logText.setMaxLines(120);
        logText.setPadding(dp(12), dp(12), dp(12), dp(12));
        logText.setBackground(rounded(COLOR_FIELD, 10, COLOR_FIELD));
        logContainer.addView(logText, matchWrap());
        card.addView(logContainer, matchWrap());
        logToggleButton.setOnClickListener(view -> {
            logVisible = !logVisible;
            toggleSection(logContainer, logVisible);
            logToggleButton.setText(logVisible ? "隐藏详细日志  ⌄" : "显示详细日志  ›");
            view.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
        });
        addCard(content, card);
    }

    private void buildNotesCard(LinearLayout content) {
        LinearLayout card = card("关于本次扫描");
        TextView note = text("目标域名应已接入待优选的 CDN。默认参数约发起 12,000 次 HTTPS 探测；"
                + "下载测速默认关闭，私有代理密码不会保存。本版不会自动修改 DNS。",
                13, COLOR_MUTED, Typeface.NORMAL);
        note.setLineSpacing(0, 1.25f);
        card.addView(note, matchWrap());
        addCard(content, card);
    }

    private LinearLayout buildActionBar() {
        LinearLayout bar = new LinearLayout(this);
        bar.setOrientation(LinearLayout.HORIZONTAL);
        bar.setPadding(dp(8), dp(8), dp(8), dp(8));
        bar.setGravity(Gravity.CENTER_VERTICAL);
        bar.setBackground(rounded(Color.WHITE, 18, Color.WHITE));
        bar.setElevation(dp(10));

        startButton = primaryButton("开始优选");
        startButton.setOnClickListener(view -> requestStart());
        stopButton = dangerButton("停止");
        stopButton.setEnabled(false);
        stopButton.setAlpha(0.45f);
        stopButton.setOnClickListener(view -> stopSearch());

        bar.addView(startButton, weightedButtonParams(2f, 8));
        bar.addView(stopButton, weightedButtonParams(1f, 0));
        return bar;
    }

    private void requestStart() {
        if (running) {
            return;
        }
        final RunConfig config;
        try {
            config = readConfig();
        } catch (IllegalArgumentException error) {
            Toast.makeText(this, error.getMessage(), Toast.LENGTH_LONG).show();
            return;
        }
        savePreferences();

        long requestCount = (long) config.budget * config.rounds;
        if (requestCount >= 5000) {
            String message = String.format(Locale.CHINA,
                    "本次大约会发起 %,d 次 HTTPS 探测，可能持续数分钟并消耗流量和电量。是否继续？",
                    requestCount);
            new AlertDialog.Builder(this)
                    .setTitle("确认开始")
                    .setMessage(message)
                    .setNegativeButton("取消", null)
                    .setPositiveButton("继续", (dialog, which) -> beginSearch(config))
                    .show();
        } else {
            beginSearch(config);
        }
    }

    private RunConfig readConfig() {
        RunConfig config = new RunConfig();
        config.ipv6 = ipVersionSpinner.getSelectedItemPosition() == 1;
        config.host = required(hostField, "请填写目标域名");
        if (!config.host.contains(".")) {
            throw new IllegalArgumentException("目标域名格式不正确");
        }
        config.path = required(pathField, "请填写请求路径");
        if (!config.path.startsWith("/")) {
            throw new IllegalArgumentException("请求路径必须以 / 开头");
        }
        config.customCidrs = cidrField.getText().toString().trim();
        config.budget = positiveInt(budgetField, "探测预算", 1, 1_000_000);
        config.concurrency = positiveInt(concurrencyField, "并发数", 1, 2000);
        config.top = positiveInt(topField, "结果数量", 1, 1000);
        config.heads = positiveInt(headsField, "搜索头数", 1, 64);
        config.timeoutSeconds = positiveDouble(timeoutField, "单轮超时", 0.1, 300);
        config.rounds = positiveInt(roundsField, "每 IP 轮数", 1, 100);
        config.skipFirst = nonNegativeInt(skipFirstField, "跳过轮数", 0, 99);
        if (config.skipFirst >= config.rounds) {
            throw new IllegalArgumentException("跳过轮数必须小于每 IP 轮数");
        }
        if (config.top > config.budget) {
            throw new IllegalArgumentException("结果数量不能大于探测预算");
        }
        config.coloMode = coloModeSpinner.getSelectedItemPosition();
        config.colo = coloField.getText().toString().trim().toUpperCase(Locale.ROOT);
        if (config.coloMode != 0 && config.colo.isEmpty()) {
            throw new IllegalArgumentException("启用节点筛选时请填写 Colo 代码");
        }

        config.downloadEnabled = downloadSwitch.isChecked();
        if (config.downloadEnabled) {
            config.downloadTop = positiveInt(downloadTopField, "测速 IP 数", 1, 100);
            config.downloadMegabytes = positiveInt(downloadMbField, "下载上限", 1, 10_000);
            config.downloadTimeoutSeconds = positiveDouble(downloadTimeoutField, "测速超时", 1, 3600);
            config.downloadUrl = downloadUrlField.getText().toString().trim();
            if (!config.downloadUrl.isEmpty() && !config.downloadUrl.startsWith("https://")) {
                throw new IllegalArgumentException("自定义下载 URL 必须以 https:// 开头");
            }
            config.downloadSequential = downloadModeSpinner.getSelectedItemPosition() == 1;
        }

        config.proxyEnabled = proxySwitch.isChecked();
        if (config.proxyEnabled) {
            config.proxyAddress = required(proxyAddressField, "请填写代理地址");
            config.proxyUsername = proxyUsernameField.getText().toString();
            config.proxyPassword = proxyPasswordField.getText().toString();
            config.proxyMethod = proxyMethodSpinner.getSelectedItem().toString();
            config.proxyTimeoutSeconds = positiveDouble(proxyTimeoutField, "代理握手超时", 0.1, 300);
        }
        return config;
    }

    private void beginSearch(RunConfig config) {
        if (running || destroyed) {
            return;
        }
        RunSession session = new RunSession();
        activeRun = session;
        running = true;
        startButton.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
        synchronized (resultLock) {
            results.clear();
            resultUpdateScheduled = false;
            resultPage = 0;
        }
        synchronized (logLock) {
            logBuffer.setLength(0);
        }
        resultText.setText("正在扫描…");
        resultPager.setVisibility(View.GONE);
        logText.setText("");
        progressBar.setProgress(0);
        statusText.setText("正在准备扫描…");
        setRunningButtons(true);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        if (mainScrollView != null && statusCard != null) {
            statusCard.post(() -> mainScrollView.smoothScrollTo(0,
                    Math.max(0, statusCard.getTop() - dp(10))));
        }
        workerPool.execute(() -> runSearch(session, config));
    }

    private void runSearch(RunSession session, RunConfig config) {
        File proxyConfig = null;
        Process child = null;
        Future<?> stdoutReader = null;
        Future<?> stderrReader = null;
        int exitCode = -1;
        Exception failure = null;
        try {
            if (session.isStopRequested()) {
                return;
            }
            File binary = new File(getApplicationInfo().nativeLibraryDir, "libmcis.so");
            if (!binary.isFile()) {
                throw new IOException("未找到内置 MCIS 核心程序");
            }
            if (!binary.canExecute() && !binary.setExecutable(true, true)) {
                throw new IOException("MCIS 核心程序没有执行权限");
            }

            File cidrFile = new File(getFilesDir(), config.ipv6 ? "scan-ipv6.txt" : "scan-ipv4.txt");
            if (config.customCidrs.isEmpty()) {
                copyAsset(config.ipv6 ? "ipv6cidr.txt" : "ipv4cidr.txt", cidrFile);
            } else {
                writePrivateFile(cidrFile, config.customCidrs + "\n");
            }

            List<String> command = new ArrayList<>();
            command.add(binary.getAbsolutePath());
            addArg(command, "--cidr-file", cidrFile.getAbsolutePath());
            addArg(command, "--host", config.host);
            addArg(command, "--path", config.path);
            addArg(command, "--budget", Integer.toString(config.budget));
            addArg(command, "--concurrency", Integer.toString(config.concurrency));
            addArg(command, "--heads", Integer.toString(config.heads));
            addArg(command, "--top", Integer.toString(config.top));
            addArg(command, "--timeout", duration(config.timeoutSeconds));
            addArg(command, "--rounds", Integer.toString(config.rounds));
            addArg(command, "--skip-first", Integer.toString(config.skipFirst));
            addArg(command, "--out", "jsonl");
            command.add("-v");

            if (config.coloMode == 1) {
                addArg(command, "--colo", config.colo);
            } else if (config.coloMode == 2) {
                addArg(command, "--colo-exclude", config.colo);
            }

            if (config.downloadEnabled) {
                addArg(command, "--download-top", Integer.toString(config.downloadTop));
                addArg(command, "--download-bytes", Long.toString(config.downloadMegabytes * 1_000_000L));
                addArg(command, "--download-timeout", duration(config.downloadTimeoutSeconds));
                addArg(command, "--download-mode", config.downloadSequential ? "sequential" : "all");
                if (!config.downloadUrl.isEmpty()) {
                    addArg(command, "--download-url", config.downloadUrl);
                }
            } else {
                addArg(command, "--download-top", "0");
            }

            if (config.proxyEnabled) {
                proxyConfig = new File(getCacheDir(), "private-socks-" + System.nanoTime() + ".json");
                JSONObject json = new JSONObject();
                json.put("address", config.proxyAddress);
                json.put("username", config.proxyUsername);
                json.put("password", config.proxyPassword);
                json.put("method", config.proxyMethod);
                json.put("handshake_timeout", duration(config.proxyTimeoutSeconds));
                writePrivateFile(proxyConfig, json.toString());
                addArg(command, "--private-socks-config", proxyConfig.getAbsolutePath());
            }

            appendLog(session, String.format(Locale.CHINA,
                    "开始：%s，预算 %d，并发 %d，每 IP %d 轮",
                    config.ipv6 ? "IPv6" : "IPv4", config.budget, config.concurrency, config.rounds));

            ProcessBuilder processBuilder = new ProcessBuilder(command);
            processBuilder.directory(getFilesDir());
            processBuilder.redirectErrorStream(false);
            Process process = processBuilder.start();
            child = process;
            // Stop/onDestroy may race with ProcessBuilder.start(). A late child
            // must be terminated by the session instead of becoming orphaned.
            if (!session.attach(process)) {
                return;
            }

            stdoutReader = workerPool.submit(() -> readStream(session, process.getInputStream(), false));
            stderrReader = workerPool.submit(() -> readStream(session, process.getErrorStream(), true));
            exitCode = process.waitFor();
            waitForReader(stdoutReader);
            waitForReader(stderrReader);
        } catch (Exception error) {
            failure = error;
            if (error instanceof InterruptedException) {
                Thread.currentThread().interrupt();
            }
            if (!session.isStopRequested()) {
                appendLog(session, "错误：" + safeMessage(error));
            }
        } finally {
            session.close();
            if (child != null) {
                closeQuietly(child.getInputStream());
                closeQuietly(child.getErrorStream());
                closeQuietly(child.getOutputStream());
            }
            if (stdoutReader != null) stdoutReader.cancel(true);
            if (stderrReader != null) stderrReader.cancel(true);
            if (proxyConfig != null && proxyConfig.exists() && !proxyConfig.delete()) {
                proxyConfig.deleteOnExit();
            }
            final int finishedCode = exitCode;
            final Exception finishedError = failure;
            runOnUiThread(() -> {
                stopButton.removeCallbacks(session.forceStopAction);
                if (!destroyed && activeRun == session) {
                    activeRun = null;
                    finishRun(finishedCode, session.isStopRequested(), finishedError);
                }
            });
        }
    }

    private void readStream(RunSession session, InputStream stream, boolean diagnostic) {
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(stream, StandardCharsets.UTF_8))) {
            String line;
            while ((line = reader.readLine()) != null) {
                if (diagnostic) {
                    appendLog(session, line);
                    updateProgress(session, line);
                } else {
                    appendResult(session, line);
                }
            }
        } catch (Exception error) {
            if (!session.isStopRequested()) {
                throw new IllegalStateException("读取输出失败：" + safeMessage(error), error);
            }
        }
    }

    private void updateProgress(RunSession session, String line) {
        Matcher matcher = PROGRESS_PATTERN.matcher(line);
        if (!matcher.find()) {
            return;
        }
        int completed;
        int total;
        try {
            completed = Integer.parseInt(matcher.group(1));
            total = Integer.parseInt(matcher.group(2));
        } catch (NumberFormatException ignored) {
            return;
        }
        int percent = total == 0 ? 0 : (int) Math.min(100, (completed * 100L) / total);
        runOnUiThread(() -> {
            if (destroyed || activeRun != session) return;
            progressBar.setProgress(percent);
            statusText.setText(String.format(Locale.CHINA, "已探测 %,d / %,d（%d%%）", completed, total, percent));
        });
    }

    private void appendResult(RunSession session, String line) throws Exception {
        JSONObject json = new JSONObject(line);
        if (!json.optBoolean("ok", false)) return;
        JSONObject trace = json.optJSONObject("trace");
        ScanResults.Row row = new ScanResults.Row(json.getString("ip"), json.optString("prefix", ""),
                trace == null ? "" : trace.optString("colo", ""), json.getDouble("score_ms"), json.optInt("status", 0),
                json.optBoolean("download_ok", false), json.optDouble("download_mbps", 0),
                json.optLong("download_ms", 0), json.optLong("download_bytes", 0), json.optString("download_error", ""));
        synchronized (resultLock) {
            if (destroyed || activeRun != session) return;
            results.add(row);
            if (resultUpdateScheduled) return;
            resultUpdateScheduled = true;
        }
        // Keep all rows, but avoid rebuilding the displayed page once for every JSONL row.
        resultText.postDelayed(() -> {
            if (destroyed || activeRun != session) return;
            synchronized (resultLock) { resultUpdateScheduled = false; }
            renderResults();
        }, 80);
    }

    private void showResultPage(int delta) {
        synchronized (resultLock) { resultPage = results.clampPage(resultPage + delta); }
        renderResults();
    }

    private void renderResults() {
        final int count, pages, page;
        final String text;
        synchronized (resultLock) {
            count = results.size();
            pages = results.pageCount();
            resultPage = results.clampPage(resultPage);
            page = resultPage;
            text = results.pageText(page);
        }
        if (count > 0) resultText.setText(text);
        resultPager.setVisibility(pages > 1 ? View.VISIBLE : View.GONE);
        resultPageText.setText(String.format(Locale.CHINA, "%d / %d\n共 %,d 条", page + 1, pages, count));
        previousResultsButton.setEnabled(page > 0);
        nextResultsButton.setEnabled(page + 1 < pages);
    }

    private void appendLog(String line) {
        appendLog(activeRun, line);
    }

    private void appendLog(RunSession session, String line) {
        if (destroyed || activeRun != session) return;
        final String snapshot;
        synchronized (logLock) {
            logBuffer.append(line).append('\n');
            trimBuffer(logBuffer, 50_000);
            snapshot = logBuffer.toString();
        }
        runOnUiThread(() -> {
            if (!destroyed && activeRun == session) logText.setText(snapshot);
        });
    }

    private void finishRun(int exitCode, boolean stopped, Exception error) {
        running = false;
        synchronized (resultLock) { resultUpdateScheduled = false; }
        renderResults();
        getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setRunningButtons(false);
        if (stopped) {
            statusText.setText("已停止");
            return;
        }
        if (error != null || exitCode != 0) {
            statusText.setText(exitCode >= 0 ? "运行失败（退出码 " + exitCode + "）" : "运行失败");
            Toast.makeText(this, "运行失败，请查看详细日志", Toast.LENGTH_LONG).show();
            return;
        }
        progressBar.setProgress(100);
        synchronized (resultLock) {
            if (results.size() == 0) {
                resultText.setText("扫描完成，但没有得到可用结果。请检查目标域名、网段或网络连接。");
                statusText.setText("扫描完成：无可用 IP");
                Toast.makeText(this, "没有可用 IP", Toast.LENGTH_SHORT).show();
                return;
            }
        }
        statusText.setText("扫描完成");
        Toast.makeText(this, "优选完成", Toast.LENGTH_SHORT).show();
    }

    private void stopSearch() {
        if (!running) {
            return;
        }
        RunSession session = activeRun;
        if (session == null) return;
        statusText.setText("正在停止…");
        appendLog("用户请求停止扫描");
        session.requestStop();
        stopButton.removeCallbacks(session.forceStopAction);
        stopButton.postDelayed(session.forceStopAction, 2000);
    }

    private void setRunningButtons(boolean isRunning) {
        startButton.setEnabled(!isRunning);
        startButton.setAlpha(isRunning ? 0.45f : 1f);
        stopButton.setEnabled(isRunning);
        stopButton.setAlpha(isRunning ? 1f : 0.45f);
    }

    private void copyResults() {
        final String value;
        synchronized (resultLock) {
            value = results.copyText().trim();
        }
        if (value.isEmpty()) {
            Toast.makeText(this, "还没有可复制的结果", Toast.LENGTH_SHORT).show();
            return;
        }
        ClipboardManager clipboard = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
        clipboard.setPrimaryClip(ClipData.newPlainText("MCIS 优选结果", value));
        Toast.makeText(this, "结果已复制", Toast.LENGTH_SHORT).show();
    }

    private void clearOutput() {
        if (running) {
            Toast.makeText(this, "扫描运行中，暂不能清空", Toast.LENGTH_SHORT).show();
            return;
        }
        synchronized (resultLock) {
            results.clear();
            resultUpdateScheduled = false;
            resultPage = 0;
        }
        synchronized (logLock) {
            logBuffer.setLength(0);
        }
        resultText.setText("运行完成后，结果会显示在这里。");
        resultPager.setVisibility(View.GONE);
        logText.setText("");
        progressBar.setProgress(0);
        statusText.setText("准备就绪");
    }

    private void savePreferences() {
        preferences.edit()
                .putInt("ip_version", ipVersionSpinner.getSelectedItemPosition())
                .putString("host", value(hostField))
                .putString("path", value(pathField))
                .putString("cidrs", cidrField.getText().toString())
                .putString("budget", value(budgetField))
                .putString("concurrency", value(concurrencyField))
                .putString("heads", value(headsField))
                .putString("top", value(topField))
                .putString("timeout", value(timeoutField))
                .putString("rounds", value(roundsField))
                .putString("skip_first", value(skipFirstField))
                .putInt("colo_mode", coloModeSpinner.getSelectedItemPosition())
                .putString("colo", value(coloField))
                .putBoolean("download_enabled", downloadSwitch.isChecked())
                .putString("download_top", value(downloadTopField))
                .putString("download_mb", value(downloadMbField))
                .putString("download_timeout", value(downloadTimeoutField))
                .putString("download_url", value(downloadUrlField))
                .putInt("download_mode", downloadModeSpinner.getSelectedItemPosition())
                .putBoolean("proxy_enabled", proxySwitch.isChecked())
                .putString("proxy_address", value(proxyAddressField))
                .putString("proxy_username", value(proxyUsernameField))
                .putInt("proxy_method", proxyMethodSpinner.getSelectedItemPosition())
                .putString("proxy_timeout", value(proxyTimeoutField))
                .apply();
    }

    private void copyAsset(String assetName, File output) throws IOException {
        try (InputStream input = getAssets().open(assetName);
             FileOutputStream target = new FileOutputStream(output, false)) {
            byte[] buffer = new byte[16 * 1024];
            int count;
            while ((count = input.read(buffer)) != -1) {
                target.write(buffer, 0, count);
            }
        }
    }

    private static void writePrivateFile(File file, String content) throws IOException {
        try (FileOutputStream output = new FileOutputStream(file, false)) {
            output.write(content.getBytes(StandardCharsets.UTF_8));
        }
        file.setReadable(false, false);
        file.setWritable(false, false);
        file.setReadable(true, true);
        file.setWritable(true, true);
    }

    private static void addArg(List<String> command, String name, String value) {
        command.add(name);
        command.add(value);
    }

    private static String duration(double seconds) {
        if (seconds == Math.rint(seconds)) {
            return String.format(Locale.US, "%.0fs", seconds);
        }
        return String.format(Locale.US, "%ss", Double.toString(seconds));
    }

    private static void waitForReader(Future<?> reader) throws Exception {
        try {
            reader.get(5, TimeUnit.SECONDS);
        } catch (Exception error) {
            reader.cancel(true);
            throw error;
        }
    }

    private static void closeQuietly(java.io.Closeable stream) {
        try {
            stream.close();
        } catch (IOException ignored) {
        }
    }

    private static void trimBuffer(StringBuilder buffer, int maximum) {
        if (buffer.length() > maximum) {
            buffer.delete(0, buffer.length() - maximum + 5000);
            buffer.insert(0, "…较早的输出已省略…\n");
        }
    }

    private static String safeMessage(Throwable error) {
        String message = error.getMessage();
        return message == null || message.trim().isEmpty() ? error.getClass().getSimpleName() : message;
    }

    private String required(EditText field, String message) {
        String value = value(field);
        if (value.isEmpty()) {
            throw new IllegalArgumentException(message);
        }
        return value;
    }

    private int positiveInt(EditText field, String label, int minimum, int maximum) {
        return nonNegativeInt(field, label, minimum, maximum);
    }

    private int nonNegativeInt(EditText field, String label, int minimum, int maximum) {
        try {
            int parsed = Integer.parseInt(value(field));
            if (parsed < minimum || parsed > maximum) {
                throw new NumberFormatException();
            }
            return parsed;
        } catch (NumberFormatException error) {
            throw new IllegalArgumentException(label + "应在 " + minimum + "–" + maximum + " 之间");
        }
    }

    private double positiveDouble(EditText field, String label, double minimum, double maximum) {
        try {
            double parsed = Double.parseDouble(value(field));
            if (!Double.isFinite(parsed) || parsed < minimum || parsed > maximum) {
                throw new NumberFormatException();
            }
            return parsed;
        } catch (NumberFormatException error) {
            throw new IllegalArgumentException(label + "应在 " + minimum + "–" + maximum + " 之间");
        }
    }

    private static String value(EditText field) {
        return field.getText().toString().trim();
    }

    private LinearLayout card(String title) {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(16), dp(15), dp(16), dp(16));
        card.setBackground(rounded(Color.WHITE, 18, Color.WHITE));
        card.setElevation(dp(1));
        TextView heading = text(title, 13, COLOR_MUTED, Typeface.BOLD);
        heading.setLetterSpacing(0.035f);
        LinearLayout.LayoutParams headingParams = matchWrap();
        headingParams.bottomMargin = dp(10);
        card.addView(heading, headingParams);
        return card;
    }

    private void addCard(LinearLayout parent, LinearLayout card) {
        LinearLayout.LayoutParams params = matchWrap();
        params.bottomMargin = dp(14);
        parent.addView(card, params);
    }

    private void addLabel(LinearLayout parent, String label) {
        TextView view = text(label, 12, COLOR_MUTED, Typeface.NORMAL);
        LinearLayout.LayoutParams params = matchWrap();
        params.topMargin = dp(9);
        params.bottomMargin = dp(5);
        parent.addView(view, params);
    }

    private void addLabeled(LinearLayout parent, String label, View input) {
        addLabel(parent, label);
        parent.addView(input, matchWrap());
    }

    private void addPair(LinearLayout parent, String leftLabel, View left,
                         String rightLabel, View right) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout leftBox = new LinearLayout(this);
        leftBox.setOrientation(LinearLayout.VERTICAL);
        addLabel(leftBox, leftLabel);
        leftBox.addView(left, matchWrap());
        LinearLayout rightBox = new LinearLayout(this);
        rightBox.setOrientation(LinearLayout.VERTICAL);
        addLabel(rightBox, rightLabel);
        rightBox.addView(right, matchWrap());
        LinearLayout.LayoutParams leftParams = new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f);
        leftParams.rightMargin = dp(8);
        row.addView(leftBox, leftParams);
        row.addView(rightBox, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        parent.addView(row, matchWrap());
    }

    private EditText numeric(String initial, String hint) {
        return input(initial, hint, InputType.TYPE_CLASS_NUMBER, true);
    }

    private EditText decimal(String initial, String hint) {
        return input(initial, hint, InputType.TYPE_CLASS_NUMBER | InputType.TYPE_NUMBER_FLAG_DECIMAL, true);
    }

    private EditText input(String initial, String hint, int inputType, boolean singleLine) {
        EditText field = new EditText(this);
        field.setText(initial);
        field.setHint(hint);
        field.setTextColor(COLOR_DARK);
        field.setHintTextColor(Color.rgb(150, 160, 174));
        field.setTextSize(15);
        field.setInputType(inputType);
        field.setSingleLine(singleLine);
        field.setMinHeight(dp(46));
        field.setPadding(dp(12), dp(9), dp(12), dp(9));
        field.setBackground(rounded(COLOR_FIELD, 11, COLOR_FIELD));
        return field;
    }

    private SegmentedControl spinner(String[] values, int selected) {
        return new SegmentedControl(values, selected);
    }

    private TextView text(String value, int sizeSp, int color, int style) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(sizeSp);
        view.setTextColor(color);
        view.setTypeface(Typeface.create(style == Typeface.BOLD
                ? "sans-serif-medium" : "sans-serif", style));
        return view;
    }

    private Button primaryButton(String text) {
        Button button = new Button(this);
        button.setText(text);
        button.setTextColor(Color.WHITE);
        button.setTextSize(15);
        button.setTypeface(Typeface.create("sans-serif-medium", Typeface.BOLD));
        button.setAllCaps(false);
        button.setMinHeight(0);
        button.setMinimumHeight(0);
        button.setPadding(dp(12), 0, dp(12), 0);
        button.setStateListAnimator(null);
        button.setBackground(rounded(COLOR_BLUE, 13, COLOR_BLUE));
        addPressEffect(button);
        return button;
    }

    private Button dangerButton(String text) {
        Button button = primaryButton(text);
        button.setBackground(rounded(COLOR_RED, 13, COLOR_RED));
        return button;
    }

    private Button secondaryButton(String text) {
        Button button = new Button(this);
        button.setText(text);
        button.setTextColor(COLOR_BLUE);
        button.setTextSize(13);
        button.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        button.setAllCaps(false);
        button.setMinHeight(0);
        button.setMinimumHeight(0);
        button.setStateListAnimator(null);
        button.setBackground(rounded(COLOR_FIELD, 11, COLOR_FIELD));
        addPressEffect(button);
        return button;
    }

    private Button disclosureButton(String value) {
        Button button = secondaryButton(value);
        button.setGravity(Gravity.CENTER_VERTICAL | Gravity.START);
        button.setPadding(dp(14), 0, dp(14), 0);
        return button;
    }

    private Switch iosSwitch(boolean checked) {
        Switch control = new Switch(this);
        control.setChecked(checked);
        control.setShowText(false);
        control.setText("");
        control.setSwitchMinWidth(dp(52));
        control.setSplitTrack(false);
        int[][] states = new int[][]{
                new int[]{android.R.attr.state_checked},
                new int[]{-android.R.attr.state_checked}
        };
        control.setTrackTintList(new ColorStateList(states,
                new int[]{COLOR_GREEN, Color.rgb(209, 209, 214)}));
        control.setThumbTintList(new ColorStateList(states,
                new int[]{Color.WHITE, Color.WHITE}));
        control.setScaleX(1.08f);
        control.setScaleY(1.08f);
        return control;
    }

    private LinearLayout toggleRow(String title, String subtitle, Switch control) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        row.setPadding(0, dp(2), 0, dp(4));
        LinearLayout labels = new LinearLayout(this);
        labels.setOrientation(LinearLayout.VERTICAL);
        labels.addView(text(title, 16, COLOR_DARK, Typeface.NORMAL), matchWrap());
        TextView secondary = text(subtitle, 12, COLOR_MUTED, Typeface.NORMAL);
        LinearLayout.LayoutParams secondaryParams = matchWrap();
        secondaryParams.topMargin = dp(3);
        labels.addView(secondary, secondaryParams);
        LinearLayout.LayoutParams labelsParams = new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f);
        labelsParams.rightMargin = dp(12);
        row.addView(labels, labelsParams);
        row.addView(control, new LinearLayout.LayoutParams(dp(58), dp(42)));
        return row;
    }

    private void toggleSection(View section, boolean visible) {
        section.animate().cancel();
        if (visible) {
            section.setAlpha(0f);
            section.setVisibility(View.VISIBLE);
            section.animate().alpha(1f).setDuration(180).start();
        } else {
            section.animate().alpha(0f).setDuration(140).withEndAction(() -> {
                section.setVisibility(View.GONE);
                section.setAlpha(1f);
            }).start();
        }
    }

    private void addPressEffect(View view) {
        view.setOnTouchListener((target, event) -> {
            if (!target.isEnabled()) {
                return false;
            }
            if (event.getAction() == MotionEvent.ACTION_DOWN) {
                target.animate().scaleX(0.975f).scaleY(0.975f).setDuration(70).start();
            } else if (event.getAction() == MotionEvent.ACTION_UP
                    || event.getAction() == MotionEvent.ACTION_CANCEL) {
                target.animate().scaleX(1f).scaleY(1f).setDuration(110).start();
            }
            return false;
        });
    }

    private GradientDrawable rounded(int fillColor, int radiusDp, int strokeColor) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fillColor);
        drawable.setCornerRadius(dp(radiusDp));
        drawable.setStroke(dp(1), strokeColor);
        return drawable;
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT);
    }

    private LinearLayout.LayoutParams weightedButtonParams(float weight, int rightMarginDp) {
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(0, dp(50), weight);
        params.rightMargin = dp(rightMarginDp);
        return params;
    }

    private LinearLayout.LayoutParams wrapWrapWithEndMargin(int endMarginDp) {
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, dp(28));
        params.rightMargin = dp(endMarginDp);
        return params;
    }

    private int dp(float value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private final class SegmentedControl extends LinearLayout {
        private final String[] labels;
        private final TextView[] segments;
        private int selectedIndex;

        SegmentedControl(String[] values, int selected) {
            super(MainActivity.this);
            labels = values.clone();
            segments = new TextView[values.length];
            selectedIndex = Math.max(0, Math.min(selected, values.length - 1));
            setOrientation(HORIZONTAL);
            setGravity(Gravity.CENTER_VERTICAL);
            setPadding(dp(3), dp(3), dp(3), dp(3));
            setBackground(rounded(Color.rgb(235, 235, 240), 11,
                    Color.rgb(235, 235, 240)));

            for (int index = 0; index < values.length; index++) {
                final int position = index;
                TextView segment = text(values[index], values.length > 2 ? 11 : 12,
                        COLOR_MUTED, Typeface.NORMAL);
                segment.setGravity(Gravity.CENTER);
                segment.setMaxLines(2);
                segment.setPadding(dp(5), 0, dp(5), 0);
                segment.setOnClickListener(view -> {
                    setSelection(position);
                    view.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
                });
                segments[index] = segment;
                addView(segment, new LinearLayout.LayoutParams(0, dp(38), 1f));
            }
            refreshSegments();
        }

        int getSelectedItemPosition() {
            return selectedIndex;
        }

        String getSelectedItem() {
            return labels[selectedIndex];
        }

        void setSelection(int position) {
            int bounded = Math.max(0, Math.min(position, labels.length - 1));
            if (selectedIndex == bounded) {
                return;
            }
            selectedIndex = bounded;
            refreshSegments();
        }

        private void refreshSegments() {
            for (int index = 0; index < segments.length; index++) {
                TextView segment = segments[index];
                if (index == selectedIndex) {
                    segment.setTextColor(COLOR_DARK);
                    segment.setTypeface(Typeface.create("sans-serif-medium", Typeface.BOLD));
                    segment.setBackground(rounded(Color.WHITE, 9, Color.WHITE));
                    segment.setElevation(dp(2));
                } else {
                    segment.setTextColor(COLOR_MUTED);
                    segment.setTypeface(Typeface.create("sans-serif", Typeface.NORMAL));
                    segment.setBackgroundColor(Color.TRANSPARENT);
                    segment.setElevation(0f);
                }
            }
        }
    }

    @Override
    public void onBackPressed() {
        if (!running) {
            super.onBackPressed();
            return;
        }
        new AlertDialog.Builder(this)
                .setTitle("扫描仍在运行")
                .setMessage("退出会停止当前扫描。")
                .setNegativeButton("继续扫描", null)
                .setPositiveButton("停止并退出", (dialog, which) -> {
                    stopSearch();
                    MainActivity.super.onBackPressed();
                })
                .show();
    }

    @Override
    protected void onDestroy() {
        destroyed = true;
        RunSession session = activeRun;
        activeRun = null;
        if (session != null) {
            stopButton.removeCallbacks(session.forceStopAction);
            session.forceStop();
            session.close();
        }
        workerPool.shutdownNow();
        super.onDestroy();
    }

    private static final class RunConfig {
        boolean ipv6;
        String host;
        String path;
        String customCidrs;
        int budget;
        int concurrency;
        int heads;
        int top;
        double timeoutSeconds;
        int rounds;
        int skipFirst;
        int coloMode;
        String colo;
        boolean downloadEnabled;
        int downloadTop;
        int downloadMegabytes;
        double downloadTimeoutSeconds;
        String downloadUrl = "";
        boolean downloadSequential;
        boolean proxyEnabled;
        String proxyAddress = "";
        String proxyUsername = "";
        String proxyPassword = "";
        String proxyMethod = "0x80";
        double proxyTimeoutSeconds;
    }
}
