package com.ztyawc.mcis;

import android.view.WindowManager;
import androidx.annotation.NonNull;
import io.flutter.embedding.android.FlutterActivity;
import io.flutter.embedding.engine.FlutterEngine;
import io.flutter.plugin.common.EventChannel;
import io.flutter.plugin.common.MethodChannel;
import java.util.Map;

/** Flutter owns presentation; NativeScanner owns the lifetime of each Go process. */
public final class FlutterMainActivity extends FlutterActivity {
    private NativeScanner scanner;

    @Override public void configureFlutterEngine(@NonNull FlutterEngine engine) {
        super.configureFlutterEngine(engine);
        scanner = new NativeScanner(this, running -> {
            if (running) getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
            else getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        });
        new EventChannel(engine.getDartExecutor().getBinaryMessenger(), "com.ztyawc.mcis/events")
                .setStreamHandler(scanner);
        new MethodChannel(engine.getDartExecutor().getBinaryMessenger(), "com.ztyawc.mcis/control")
                .setMethodCallHandler((call, result) -> {
                    try {
                        switch (call.method) {
                            case "settings": result.success(scanner.settings()); break;
                            case "saveSettings": scanner.saveSettings((Map<?, ?>) call.arguments); result.success(null); break;
                            case "start": scanner.start((Map<?, ?>) call.arguments); result.success(null); break;
                            case "stop": scanner.stop(); result.success(null); break;
                            case "snapshot": result.success(scanner.snapshot()); break;
                            case "history": scanner.history(result); break;
                            case "historyEntry": scanner.historyEntry((Map<?, ?>) call.arguments, result); break;
                            case "deleteHistory": scanner.deleteHistory((Map<?, ?>) call.arguments, result); break;
                            default: result.notImplemented();
                        }
                    } catch (Exception error) {
                        result.error("mcis", error.getMessage(), null);
                    }
                });
    }

    @Override protected void onDestroy() {
        if (scanner != null) scanner.close();
        super.onDestroy();
    }
}
