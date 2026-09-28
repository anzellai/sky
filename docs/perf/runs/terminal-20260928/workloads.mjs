// docs/perf/runs/terminal-20260928/workloads.mjs
//
// The four terminal workloads of this run, as the exact bytes a PTY hands
// the server (a PTY turns "\n" into "\r\n", so the streams carry "\r\n").
// Deterministic: the same call gives the same bytes on every machine.
//
//   yes      "y\r\n" repeated to 4 MiB (what `yes` prints)
//   seq      "1\r\n" .. "200000\r\n" (what `seq 1 200000` prints)
//   redraw   300 full-screen redraws, each "clear" then a 38-line coloured
//            `ls -la` listing (what `while :; do clear; ls -la; done` prints)
//   colour   60 full 120x40 screens where every cell has its own 256-colour
//            foreground and background (an ANSI colour stress test)
//
// `node workloads.mjs <dir>` writes <dir>/<name>.bin for each.
import { writeFileSync, mkdirSync } from "node:fs";

export const COLS = 120;
export const ROWS = 40;

function yes() {
  return Buffer.from("y\r\n".repeat(Math.floor((4 << 20) / 3)), "latin1");
}

function seq() {
  const parts = [];
  for (let i = 1; i <= 200000; i++) parts.push(i + "\r\n");
  return Buffer.from(parts.join(""), "latin1");
}

function redraw() {
  const out = [];
  for (let f = 0; f < 300; f++) {
    out.push("\x1b[H\x1b[2J");
    out.push("total " + (1000 + f) + "\r\n");
    for (let i = 0; i < 37; i++) {
      const dir = i % 5 === 0;
      const size = String(((i * 7919 + f * 31) % 99999) + 1).padStart(6, " ");
      const name = (dir ? "dir" : "file") + "_" + String(i).padStart(2, "0") + (dir ? "" : ".txt");
      out.push(
        (dir ? "drwxr-xr-x" : "-rw-r--r--") +
          "  1 user  staff  " +
          size +
          " Sep 28 12:" +
          String(i % 60).padStart(2, "0") +
          " " +
          (dir ? "\x1b[1;34m" + name + "\x1b[0m" : name) +
          "\r\n"
      );
    }
  }
  return Buffer.from(out.join(""), "latin1");
}

function colour() {
  const out = [];
  const glyphs = "abcdefghijklmnopqrstuvwxyz0123456789#@%&*+=";
  for (let f = 0; f < 60; f++) {
    out.push("\x1b[H");
    for (let y = 0; y < ROWS; y++) {
      for (let x = 0; x < COLS; x++) {
        out.push("\x1b[38;5;" + ((x + y + f) % 256) + ";48;5;" + ((x * y + f * 7) % 256) + "m" + glyphs[(x + y * 3 + f) % glyphs.length]);
      }
      out.push("\x1b[0m");
      if (y < ROWS - 1) out.push("\r\n");
    }
  }
  return Buffer.from(out.join(""), "latin1");
}

export const WORKLOADS = { yes, seq, redraw, colour };

if (import.meta.url === "file://" + process.argv[1]) {
  const dir = process.argv[2];
  if (!dir) {
    console.error("usage: node workloads.mjs <dir>");
    process.exit(2);
  }
  mkdirSync(dir, { recursive: true });
  for (const [name, fn] of Object.entries(WORKLOADS)) {
    const b = fn();
    writeFileSync(dir + "/" + name + ".bin", b);
    console.log(name, b.length);
  }
}
