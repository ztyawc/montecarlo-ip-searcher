package com.ztyawc.mcis;

import java.io.File;
import java.lang.reflect.*;
import java.util.*;

/** Runs on a JVM with android.jar and flutter.jar, without an emulator or network. */
public final class NativeConfigTest {
    private static final Class<?> TYPE;
    private static final Constructor<?> CONSTRUCTOR;
    static {
        try { TYPE = Class.forName("com.ztyawc.mcis.NativeScanner$Config"); CONSTRUCTOR = TYPE.getDeclaredConstructor(Map.class); CONSTRUCTOR.setAccessible(true); }
        catch (Exception e) { throw new ExceptionInInitializerError(e); }
    }
    private static Map<String,Object> defaults() {
        Map<String,Object> value = new HashMap<>();
        value.put("ip_version", 0); value.put("colo_mode", 0);
        value.put("budget", "2000"); value.put("concurrency", "200"); value.put("top", "20");
        value.put("heads", "4"); value.put("rounds", "6"); value.put("skip_first", "1");
        value.put("timeout", "3"); value.put("host", "www.cloudflare.com"); value.put("path", "/cdn-cgi/trace");
        return value;
    }
    private static void reject(Map<String,Object> value) throws Exception {
        try { CONSTRUCTOR.newInstance(value); throw new AssertionError("accepted invalid config"); }
        catch (InvocationTargetException e) { if (!(e.getCause() instanceof IllegalArgumentException)) throw e; }
    }
    public static void main(String[] args) throws Exception {
        Map<String,Object> value = defaults();
        Object config = CONSTRUCTOR.newInstance(value);
        Method command = TYPE.getDeclaredMethod("command", File.class, File.class); command.setAccessible(true);
        @SuppressWarnings("unchecked") List<String> argv = (List<String>)command.invoke(config, new File("/native/libmcis.so"), new File("/private/scan.txt"));
        if (!argv.contains("jsonl") || !argv.contains("--download-top") || argv.contains("--private-socks-config")) throw new AssertionError(argv);
        value.put("budget", "1.0"); reject(value);
        value = defaults(); value.put("timeout", "NaN"); reject(value);
        value = defaults(); value.put("rounds", "1"); reject(value);
        value = defaults(); value.put("budget", "5"); reject(value);
        value = defaults(); value.put("download_enabled", true); value.put("download_top", "3"); value.put("download_mb", "10"); value.put("download_timeout", "45"); value.put("download_mode", 0); value.put("download_url", "http://example.com/file"); reject(value);
        value = defaults(); value.put("proxy_enabled", true); value.put("proxy_address", "127.0.0.1:1080"); value.put("proxy_username", "short"); value.put("proxy_password", "test-secret"); reject(value);
        value.put("proxy_username", "1234567890123456789"); value.put("proxy_timeout", "10"); value.put("proxy_method", 1);
        config = CONSTRUCTOR.newInstance(value);
        @SuppressWarnings("unchecked") List<String> proxyArgs = (List<String>)command.invoke(config, new File("/native/libmcis.so"), new File("/private/scan.txt"));
        if (proxyArgs.toString().contains("test-secret")) throw new AssertionError("password leaked to argv");
        Field password = TYPE.getDeclaredField("password"); password.setAccessible(true);
        Field settings = TYPE.getDeclaredField("values"); settings.setAccessible(true);
        if (!password.get(config).equals("test-secret") || ((Map<?, ?>) settings.get(config)).containsKey("proxy_password")) throw new AssertionError("enabled proxy credential ownership is incorrect");
        value.put("proxy_enabled", false); value.put("proxy_password", "2000");
        config = CONSTRUCTOR.newInstance(value);
        if (!password.get(config).equals("") || ((Map<?, ?>) settings.get(config)).containsKey("proxy_password")) throw new AssertionError("disabled proxy retained a password");
        System.out.println("NativeConfigTest: 10 cases passed");
    }
}
