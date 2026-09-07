package com.ztyawc.mcis;

/** Owns exactly one child process. No delayed action can reach another run. */
final class RunSession {
    private Process process;
    private boolean stopRequested;
    private boolean closed;
    final Runnable forceStopAction = this::forceStop;

    synchronized boolean attach(Process child) {
        if (closed || stopRequested || process != null) {
            child.destroyForcibly();
            return false;
        }
        process = child;
        return true;
    }

    synchronized boolean isStopRequested() {
        return stopRequested;
    }

    synchronized void requestStop() {
        stopRequested = true;
        if (process != null && process.isAlive()) {
            process.destroy();
        }
    }

    synchronized void forceStop() {
        stopRequested = true;
        if (process != null && process.isAlive()) {
            process.destroyForcibly();
        }
    }

    synchronized void close() {
        closed = true;
        if (process != null && process.isAlive()) {
            process.destroyForcibly();
        }
        process = null;
    }
}
