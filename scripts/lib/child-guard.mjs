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
// SIGKILL of the harness itself cannot be caught; the shell wrapper
// (`scripts/lib/with-timeout.sh`) kills the whole process group for that case.
// `rust/crates/xtask/tests/e2e_scripts_clean_up_their_servers.rs` fails the
// build on a `spawn(` in scripts/ that is not wrapped in `guardChild(`.

const live = new Set();
let installed = false;

function killAll() {
  for (const child of live) {
    try {
      if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    } catch (_) {
      // Already gone.
    }
  }
  live.clear();
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
