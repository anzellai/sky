// scripts/lib/child-guard.mjs — every process a harness spawns dies with it.
//
// A verify script spawns the app under test and stops it in a `finally`. That
// covers the normal path only. When the script dies another way (a signal
// from the shell or CI, `process.exit` from a check, an uncaught error or a
// rejected promise before the `try`), the app is orphaned and keeps its port:
// an e2e gate once left its app server listening for hours. `guardChild` puts
// the child in a registry that is emptied on every exit path:
//
//   * the `exit` event, which Node emits for `process.exit()`, for the end of
//     the event loop and for an uncaught exception or unhandled rejection;
//   * SIGINT / SIGTERM / SIGHUP, which by default end Node WITHOUT an `exit`
//     event, so they are caught here and turned into `process.exit(128 + n)`.
//
// The guard asks first: SIGTERM, then up to TERM_GRACE_MS for the child to
// end, then SIGKILL. A Sky app ends the processes it started itself (a
// managed llama-server runs in a process group of its own) on SIGTERM, and a
// bare SIGKILL left those running under pid 1. The wait is synchronous
// (`exit` handlers cannot await), and a child that has exited but is not yet
// reaped (a zombie: the event loop that reaps it is not running here) counts
// as gone.
//
// SIGKILL of the harness itself cannot be caught; the shell wrapper
// (`scripts/lib/with-timeout.sh`) kills the whole process group for that case.
// `rust/crates/xtask/tests/e2e_scripts_clean_up_their_servers.rs` fails the
// build on a `spawn(` in scripts/ that is not wrapped in `guardChild(`.

import { execFileSync } from "node:child_process";

const TERM_GRACE_MS = 5000;
const live = new Set();
let installed = false;

function running(child) {
  return child.exitCode === null && child.signalCode === null;
}

// Whether `pid` still runs: it exists and is not a zombie.
function pidRuns(pid) {
  try {
    process.kill(pid, 0);
  } catch (_) {
    return false;
  }
  try {
    const stat = execFileSync("ps", ["-o", "stat=", "-p", String(pid)], { encoding: "utf8" });
    return stat.trim() !== "" && !stat.trim().startsWith("Z");
  } catch (_) {
    return false;
  }
}

function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function killAll() {
  const pending = [...live].filter(running);
  live.clear();
  for (const child of pending) {
    try {
      child.kill("SIGTERM");
    } catch (_) {
      // Already gone.
    }
  }
  const deadline = Date.now() + TERM_GRACE_MS;
  let left = pending;
  while (left.length > 0 && Date.now() < deadline) {
    left = left.filter((c) => pidRuns(c.pid));
    if (left.length > 0) sleepSync(50);
  }
  for (const child of left) {
    try {
      child.kill("SIGKILL");
    } catch (_) {
      // Already gone.
    }
  }
}

function install() {
  process.on("exit", killAll);
  for (const [sig, n] of [
    ["SIGINT", 2],
    ["SIGTERM", 15],
    ["SIGHUP", 1],
  ]) {
    process.on(sig, () => {
      killAll();
      process.exit(128 + n);
    });
  }
}

/** Register `child` to be killed when this process exits; returns `child`. */
export function guardChild(child) {
  if (!installed) {
    install();
    installed = true;
  }
  live.add(child);
  child.once("exit", () => live.delete(child));
  return child;
}
