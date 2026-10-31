#!/usr/bin/env bash
#
# scripts/office-second-reader-e2e.sh — files written by Std.Office.Xlsx and
# Std.Office.Docx open in other programs.
#
# Builds scripts/fixtures/office-files (a CLI that writes a ledger workbook
# and an invoice document, then reads both back from disk with File.readFile)
# and opens the two files with readers that share no code with Sky:
#
#   soffice  LibreOffice headless converts ledger.xlsx to CSV and
#            invoice.docx to text; the values and the computed total
#            (=SUM, written with no cached value) must be there. Then
#            LibreOffice saves both files again, and Sky reads what
#            LibreOffice wrote (the read direction).
#   macOS    textutil (Cocoa's .docx importer) reads invoice.docx, and
#            mdimport (Spotlight's Office importer) reads ledger.xlsx.
#            Checked on Darwin only, where both ship with the system.
#
# The Go tests (runtime-go/rt/office_*_test.go) carry a struct-decoding
# second reader that runs everywhere; this gate adds real applications.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, soffice. Without
# LibreOffice, SKY_LIVE_TESTS=skip skips the soffice section by name; the
# macOS section still runs.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "office-second-reader-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool go "install Go 1.25+"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/office-e2e.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/app" "$TMP/out"
cp -Rf "$ROOT/scripts/fixtures/office-files/." "$TMP/app/"

echo "==> building the office-files fixture"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "office-second-reader-e2e: build failed" >&2; exit 1; }

echo "==> writing ledger.xlsx and invoice.docx, reading them back"
with_timeout 120 "$TMP/app/sky-out/app" "$TMP/out" >"$TMP/run.log" 2>&1 \
  || { cat "$TMP/run.log" >&2; echo "office-second-reader-e2e: the fixture failed" >&2; exit 1; }
grep -q "xlsx sheets 1" "$TMP/run.log" || { cat "$TMP/run.log" >&2; echo "office-second-reader-e2e: Sky did not read its own workbook" >&2; exit 1; }
grep -q "Design	1,200.00" "$TMP/run.log" || { cat "$TMP/run.log" >&2; echo "office-second-reader-e2e: Sky did not read its own document" >&2; exit 1; }

fail=0
expect() { # expect <label> <file> <text>
  if grep -qF -- "$3" "$2"; then
    echo "  ok    $1: $3"
  else
    echo "  FAIL  $1: \"$3\" not found in:" >&2
    cat "$2" >&2
    fail=1
  fi
}

if require_tool soffice "install LibreOffice (soffice on PATH) to open the files in a second application"; then
  echo "==> LibreOffice"
  ( cd "$TMP/out" && with_timeout 180 soffice --headless --norestore --convert-to csv ledger.xlsx --outdir "$TMP/lo" ) >"$TMP/lo.log" 2>&1 \
    || { cat "$TMP/lo.log" >&2; echo "office-second-reader-e2e: soffice could not convert ledger.xlsx" >&2; exit 1; }
  ( cd "$TMP/out" && with_timeout 180 soffice --headless --norestore --convert-to txt:Text invoice.docx --outdir "$TMP/lo" ) >>"$TMP/lo.log" 2>&1 \
    || { cat "$TMP/lo.log" >&2; echo "office-second-reader-e2e: soffice could not convert invoice.docx" >&2; exit 1; }
  expect "soffice xlsx" "$TMP/lo/ledger.csv" "Sales"
  expect "soffice xlsx" "$TMP/lo/ledger.csv" "2026-10-01"
  # The total is a formula written with no cached value: LibreOffice computes it.
  # Its CSV export writes the raw value, not the number format.
  expect "soffice xlsx" "$TMP/lo/ledger.csv" ",Total,10100"
  expect "soffice docx" "$TMP/lo/invoice.txt" "Invoice 2026-041"
  expect "soffice docx" "$TMP/lo/invoice.txt" "Pay within 30 days."

  # The other direction: LibreOffice saves both files again in its own
  # writer, and Sky reads what LibreOffice wrote.
  echo "==> Sky reads files LibreOffice wrote"
  mkdir -p "$TMP/lo-saved"
  ( cd "$TMP/out" && with_timeout 180 soffice --headless --norestore --convert-to 'xlsx:Calc MS Excel 2007 XML' ledger.xlsx --outdir "$TMP/lo-saved" ) >"$TMP/lo-save.log" 2>&1 \
    || { cat "$TMP/lo-save.log" >&2; echo "office-second-reader-e2e: soffice could not save ledger.xlsx" >&2; exit 1; }
  ( cd "$TMP/out" && with_timeout 180 soffice --headless --norestore --convert-to 'docx:MS Word 2007 XML' invoice.docx --outdir "$TMP/lo-saved" ) >>"$TMP/lo-save.log" 2>&1 \
    || { cat "$TMP/lo-save.log" >&2; echo "office-second-reader-e2e: soffice could not save invoice.docx" >&2; exit 1; }
  with_timeout 120 "$TMP/app/sky-out/app" read "$TMP/lo-saved" >"$TMP/lo-read.log" 2>&1 \
    || { cat "$TMP/lo-read.log" >&2; echo "office-second-reader-e2e: Sky could not read the files LibreOffice wrote" >&2; exit 1; }
  expect "sky reads soffice xlsx" "$TMP/lo-read.log" "sheet Ledger frozen rows 1"
  expect "sky reads soffice xlsx" "$TMP/lo-read.log" "row Date|Account|Amount"
  expect "sky reads soffice xlsx" "$TMP/lo-read.log" "row 2026-10-01|Sales|12500"
  expect "sky reads soffice xlsx" "$TMP/lo-read.log" "row 2026-10-03|Rent|-2400"
  # LibreOffice stores the value it computed, so the formula now has one.
  expect "sky reads soffice xlsx" "$TMP/lo-read.log" "=SUM(C2:C3) -> 10100"
  expect "sky reads soffice docx" "$TMP/lo-read.log" "Invoice 2026-041"
  expect "sky reads soffice docx" "$TMP/lo-read.log" "Design"
  expect "sky reads soffice docx" "$TMP/lo-read.log" "1,200.00"
  expect "sky reads soffice docx" "$TMP/lo-read.log" "Pay within 30 days."
fi

if [ "$(uname -s)" = "Darwin" ]; then
  require_tool textutil "textutil ships with macOS"
  require_tool mdimport "mdimport ships with macOS"
  echo "==> macOS textutil and Spotlight"
  with_timeout 60 textutil -convert txt -stdout "$TMP/out/invoice.docx" >"$TMP/textutil.txt" 2>&1 || true
  expect "textutil docx" "$TMP/textutil.txt" "Invoice 2026-041"
  expect "textutil docx" "$TMP/textutil.txt" "1,200.00"
  expect "textutil docx" "$TMP/textutil.txt" "Pay at example.com/pay"
  with_timeout 60 mdimport -t -d3 "$TMP/out/ledger.xlsx" >"$TMP/mdimport.txt" 2>&1 || true
  expect "mdimport xlsx" "$TMP/mdimport.txt" 'kMDItemTextContent = "Ledger\nDate\nAccount\nAmount\nSales\nRent\nTotal\n"'
fi

if [ "$fail" -ne 0 ]; then
  echo "office-second-reader-e2e: FAILED" >&2
  exit 1
fi
echo "office-second-reader-e2e: PASS"
