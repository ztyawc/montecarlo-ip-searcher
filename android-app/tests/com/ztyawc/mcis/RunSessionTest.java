package com.ztyawc.mcis;

import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.concurrent.CountDownLatch;

/** Offline JVM regression tests; no Android runtime, credentials, or network. */
public final class RunSessionTest {
    public static void main(String[] args) throws Exception {
        delayedStopCannotKillNewRun();
        stopBeforeStartRejectsLateProcess();
        destroyBeforeStartRejectsLateProcess();
        cleanupTerminatesExceptionalRun();
        onlyOneProcessCanAttach();
        normalCompletionIsNotCancellation();
        stopAndAttachRace();
        System.out.println("PASS: 7 RunSession regression tests (including 200 concurrent races)");
    }

    private static void delayedStopCannotKillNewRun() {
        RunSession oldRun = new RunSession();
        FakeProcess oldChild = new FakeProcess();
        check(oldRun.attach(oldChild), "old process should attach");
        oldRun.requestStop();
        oldChild.complete();
        oldRun.close();
        RunSession newRun = new RunSession();
        FakeProcess newChild = new FakeProcess();
        check(newRun.attach(newChild), "new process should attach");
        oldRun.forceStopAction.run();
        check(newChild.isAlive() && newChild.forced == 0, "old callback killed new task");
        newRun.close();
    }

    private static void stopBeforeStartRejectsLateProcess() {
        RunSession run = new RunSession();
        run.requestStop();
        FakeProcess late = new FakeProcess();
        check(!run.attach(late) && !late.isAlive(), "late process survived stop");
    }

    private static void destroyBeforeStartRejectsLateProcess() {
        RunSession run = new RunSession();
        run.forceStop();
        run.close();
        FakeProcess late = new FakeProcess();
        check(!run.attach(late) && !late.isAlive(), "late process survived destruction");
    }

    private static void cleanupTerminatesExceptionalRun() {
        RunSession run = new RunSession();
        FakeProcess child = new FakeProcess();
        run.attach(child);
        run.close();
        run.close();
        check(!child.isAlive() && child.forced == 1, "cleanup must be effective and idempotent");
    }

    private static void onlyOneProcessCanAttach() {
        RunSession run = new RunSession();
        FakeProcess first = new FakeProcess();
        FakeProcess second = new FakeProcess();
        check(run.attach(first), "first attachment rejected");
        check(!run.attach(second) && !second.isAlive(), "second attachment accepted");
        check(first.isAlive(), "wrong child killed");
        run.close();
    }

    private static void normalCompletionIsNotCancellation() {
        RunSession run = new RunSession();
        FakeProcess child = new FakeProcess();
        run.attach(child);
        child.complete();
        run.close();
        check(!run.isStopRequested() && child.forced == 0, "normal completion marked canceled");
    }

    private static void stopAndAttachRace() throws Exception {
        for (int i = 0; i < 200; i++) {
            RunSession run = new RunSession();
            FakeProcess child = new FakeProcess();
            CountDownLatch start = new CountDownLatch(1);
            Thread attach = new Thread(() -> { await(start); run.attach(child); });
            Thread stop = new Thread(() -> { await(start); run.forceStop(); });
            attach.start();
            stop.start();
            start.countDown();
            attach.join(1000);
            stop.join(1000);
            check(!attach.isAlive() && !stop.isAlive(), "session deadlocked");
            check(!child.isAlive(), "child escaped concurrent cancellation");
            run.close();
        }
    }

    private static void await(CountDownLatch latch) {
        try { latch.await(); }
        catch (InterruptedException error) { throw new AssertionError(error); }
    }

    private static void check(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static final class FakeProcess extends Process {
        private boolean alive = true;
        private int forced;
        synchronized void complete() { alive = false; }
        @Override public synchronized boolean isAlive() { return alive; }
        @Override public synchronized void destroy() { /* Simulate delayed SIGTERM handling. */ }
        @Override public synchronized Process destroyForcibly() { forced++; alive = false; return this; }
        @Override public int waitFor() { return 0; }
        @Override public synchronized int exitValue() {
            if (alive) throw new IllegalThreadStateException("still running");
            return 0;
        }
        @Override public OutputStream getOutputStream() { return new ByteArrayOutputStream(); }
        @Override public InputStream getInputStream() { return new ByteArrayInputStream(new byte[0]); }
        @Override public InputStream getErrorStream() { return new ByteArrayInputStream(new byte[0]); }
    }
}
