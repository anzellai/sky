#!/usr/bin/env python3
"""scripts/tui-e2e-drive.py — drive the tui-e2e fixtures in a real pty.

usage: tui-e2e-drive.py <app-dir> <app-binary> <string-dir> <string-binary>

Each scenario starts the binary under a pseudo-terminal, writes key bytes
(some deliberately split across writes), and reads the screen back through
pyte, a VT100 emulator. Prints one PASS / FAIL line per check and exits 1 when
any check fails. Called by scripts/tui-e2e.sh.
"""
import fcntl
import os
import pty
import select
import signal
import sqlite3
import struct
import subprocess
import sys
import termios
import time

import pyte

COLS, ROWS = 100, 40
failures = []


class Pty:
    def __init__(self, cwd, binary):
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        env = dict(os.environ, TERM="xterm-256color")
        env.pop("NO_COLOR", None)
        self.proc = subprocess.Popen(
            [binary], cwd=cwd, stdin=slave, stdout=slave, stderr=slave, env=env,
            start_new_session=True,
            preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0))
        os.close(slave)
        self.screen = pyte.Screen(COLS, ROWS)
        self.stream = pyte.ByteStream(self.screen)

    def pump(self, secs):
        end = time.time() + secs
        while True:
            rem = end - time.time()
            if rem <= 0:
                return
            r, _, _ = select.select([self.master], [], [], rem)
            if self.master in r:
                try:
                    data = os.read(self.master, 65536)
                except OSError:
                    return
                if not data:
                    return
                self.stream.feed(data)

    def write(self, data, settle=0.15):
        try:
            os.write(self.master, data)
        except OSError:
            return  # the program already exited; the checks report it
        self.pump(settle)

    def text(self):
        return "\n".join(line.rstrip() for line in self.screen.display)

    def wait_for(self, needle, secs=5.0):
        end = time.time() + secs
        while time.time() < end:
            if needle in self.text():
                return True
            self.pump(0.1)
        return needle in self.text()

    def wait_exit(self, secs=4.0):
        end = time.time() + secs
        while time.time() < end:
            if self.proc.poll() is not None:
                return self.proc.returncode
            self.pump(0.1)
        return self.proc.poll()

    def close(self):
        if self.proc.poll() is None:
            os.killpg(self.proc.pid, signal.SIGKILL)
            self.proc.wait()
        try:
            os.close(self.master)
        except OSError:
            pass


def check(name, ok, screen=""):
    if ok:
        print("PASS  " + name)
    else:
        print("FAIL  " + name)
        if screen:
            print("      screen:\n" + "\n".join("      | " + l for l in screen.splitlines() if l.strip()))
        failures.append(name)


def fresh(app_dir):
    for f in ("e2e.db", "e2e.db-wal", "e2e.db-shm"):
        try:
            os.remove(os.path.join(app_dir, f))
        except FileNotFoundError:
            pass


def main():
    app_dir, app_bin, str_dir, str_bin = sys.argv[1:5]

    # 1. Two queued Enters act on the frame after the first (T2); the focused
    #    button keeps its label (T5); Ctrl-C quits although onKey is set (T15).
    fresh(app_dir)
    p = Pty(app_dir, app_bin)
    try:
        started = p.wait_for("items=A,B,C")
        check("app starts", started, p.text())
        p.write(b"\r\r", settle=0.5)
        ok = p.wait_for("items=C", 3)
        check("queued Enter keys act on the current frame (items=C)", ok and "items=B" not in p.text(), p.text())
        check("focused button keeps its label (Del C visible)", "Del C" in p.text(), p.text())
        p.write(b"\x03")
        check("Ctrl-C quits with an onKey handler", p.wait_exit(3) is not None, p.text())
    finally:
        p.close()

    # 2. The fill-sized input stays one row (END on screen, T4); split reads
    #    decode whole keys (T14); a slow Sub.every fires under fast ticks (T10).
    fresh(app_dir)
    p = Pty(app_dir, app_bin)
    try:
        p.wait_for("items=A,B,C")
        check("content below a Ui.width input is on screen (END)", p.wait_for("END", 2), p.text())
        p.write(b"\t\t\t")
        p.write(b"\xc3", settle=0.02)
        p.write(b"\xa9")
        check("UTF-8 rune split across reads (name=é)", p.wait_for("name=é", 3), p.text())
        p.write(b"\x1b[200~hi\x1b[20", settle=0.02)
        p.write(b"1~", settle=0.05)
        p.write(b"!")
        check("bracketed paste end marker split across reads", p.wait_for("name=éhi!", 3), p.text())
        p.write(b"\x1b[", settle=0.01)
        p.write(b"D", settle=0.05)
        p.write(b"?")
        check("escape sequence split across reads (Left arrow)", p.wait_for("name=éhi?!", 3), p.text())
        check("slow Sub.every fires under fast ticks (slowOk=yes)", p.wait_for("slowOk=yes", 4), p.text())
        p.write(b"\x03")
        p.wait_exit(3)
    finally:
        p.close()

    # 3. Cmd.publish reaches the app's own subscriber (T11); Alt+b reaches onKey
    #    (T18); withDurable restores the model on the next start (T1).
    fresh(app_dir)
    p = Pty(app_dir, app_bin)
    try:
        p.wait_for("items=A,B,C")
        p.write(b"\t\t\t\t")
        p.write(b"\r")
        check("Cmd.publish delivered to Sub.subscribeTopic (heard=ping)", p.wait_for("heard=ping", 3), p.text())
        p.write(b"\x1bb")
        check("Alt+b decoded as alt key and delivered to onKey (bumps=1)", p.wait_for("bumps=1", 3), p.text())
        p.write(b"\x03")
        p.wait_exit(3)
    finally:
        p.close()
    p = Pty(app_dir, app_bin)
    try:
        check("withDurable restores the model on restart (bumps=1)", p.wait_for("bumps=1", 4), p.text())
        p.write(b"\x03")
        p.wait_exit(3)
    finally:
        p.close()

    # 3b. A snapshot the codec can no longer decode is NOT silently replaced
    #     (SA-8): the app boots from init and never overwrites the stored row.
    db = sqlite3.connect(os.path.join(app_dir, "e2e.db"))
    db.execute("UPDATE _sky_durable_snapshot SET model_json = ?", ('{"legacy":1}',))
    db.commit()
    db.close()
    p = Pty(app_dir, app_bin)
    try:
        check("undecodable snapshot boots from init (bumps=0)", p.wait_for("bumps=0", 4), p.text())
        p.write(b"\x1bb")
        p.wait_for("bumps=1", 3)
        p.pump(0.5)
        p.write(b"\x03")
        p.wait_exit(3)
    finally:
        p.close()
    db = sqlite3.connect(os.path.join(app_dir, "e2e.db"))
    rows = [r[0] for r in db.execute("SELECT model_json FROM _sky_durable_snapshot")]
    db.close()
    check("undecodable snapshot kept unchanged after updates", rows == ['{"legacy":1}'], "rows: %r" % rows)

    # 4. App.tui String view, no onKey: starts, draws lines at column 0 in raw
    #    mode (T13), quits on q with status 0 (T16).
    p = Pty(str_dir, str_bin)
    try:
        started = p.wait_for("line two", 3)
        lines = p.text().splitlines()
        check("App.tui without onKey starts", started, p.text())
        check("String view lines start at column 0",
              len(lines) > 2 and lines[0].startswith("line one") and lines[1].startswith("line two")
              and lines[2].startswith("count=0"), p.text())
        p.write(b"q")
        rc = p.wait_exit(3)
        check("q quits an App.tui without onKey (exit 0)", rc == 0, p.text())
    finally:
        p.close()

    if failures:
        print("tui-e2e-drive: %d check(s) FAILED" % len(failures))
        sys.exit(1)
    print("tui-e2e-drive: all checks passed")


if __name__ == "__main__":
    main()
