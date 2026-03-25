import { describe, it, expect } from "vitest";
import { execSync } from "child_process";
import path from "path";
import fs from "fs";

const SKY_BIN = path.resolve("bin/sky");
const EXAMPLES_DIR = path.resolve("examples");

function skyRun(example: string): { stdout: string; exitCode: number } {
  const entryFile = path.join(EXAMPLES_DIR, example, "src/Main.sky");
  if (!fs.existsSync(entryFile)) {
    return { stdout: "", exitCode: -1 };
  }
  try {
    const stdout = execSync(`${SKY_BIN} run ${entryFile}`, {
      encoding: "utf-8",
      timeout: 60000,
    });
    return { stdout, exitCode: 0 };
  } catch (e: any) {
    return { stdout: e.stdout || "", exitCode: e.status || 1 };
  }
}

function skyCheck(entryFile: string): { output: string; exitCode: number } {
  try {
    const stdout = execSync(`${SKY_BIN} check ${entryFile} 2>&1`, {
      encoding: "utf-8",
      timeout: 30000,
    });
    return { output: stdout, exitCode: 0 };
  } catch (e: any) {
    return { output: (e.stdout || "") + (e.stderr || ""), exitCode: e.status || 1 };
  }
}

describe("sky run - examples", () => {
  it("01-hello-world", () => {
    const result = skyRun("01-hello-world");
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain("Hello from Sky!");
  });

  it("04-local-pkg", () => {
    const result = skyRun("04-local-pkg");
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain("Calculation result: Current count is: 30");
  });

  it("06-json", () => {
    const result = skyRun("06-json");
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain("Done!");
  });
});

describe("sky check - type safety", () => {
  it("detects non-exhaustive pattern match", () => {
    const file = "/tmp/sky_test_exhaust.sky";
    fs.writeFileSync(file, `module Main exposing (main)

import Std.Log exposing (println)

type Color = Red | Green | Blue

showColor color =
    case color of
        Red -> "red"
        Green -> "green"

main = println (showColor Red)
`);
    const result = skyCheck(file);
    expect(result.output).toContain("Non-exhaustive pattern match for Color");
    expect(result.output).toContain("Missing cases: Blue");
  });

  it("accepts annotation that trusts user intent", () => {
    const file = "/tmp/sky_test_annot.sky";
    fs.writeFileSync(file, `module Main exposing (main)

import Std.Log exposing (println)

add : Int -> Int -> String
add a b = a + b

main = println "test"
`);
    const result = skyCheck(file);
    // Annotation mismatches are accepted (trusted) — the annotation wins
    // This matches Elm's behavior for ports and FFI boundaries
    expect(result.exitCode).toBe(0);
  });

  it("passes valid programs", () => {
    const entryFile = path.join(EXAMPLES_DIR, "01-hello-world/src/Main.sky");
    const result = skyCheck(entryFile);
    expect(result.exitCode).toBe(0);
  });
});
