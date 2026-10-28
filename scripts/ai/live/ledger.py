#!/usr/bin/env python3
"""ledger.py: the books of scripts/ai/gcp-live.sh (no network, no cloud call).

    ledger.py worst PRICES STAGES          this run's worst case in USD
    ledger.py guard LEDGER WORST CAP       exit 4 when the ledger has no room
    ledger.py stage-seconds PRICES STAGE   the wall-clock window of one stage
    ledger.py cancel-wait PRICES           the most seconds a stage's cleanup
                                           waits for cancelled jobs to end
    ledger.py append --ledger ... --stage-log ... --measure-log ... (see below)
                                           append the run's ledger line

The worst cases and the windows are in gcp-prices.json ("stageWorstCaseUsd",
"stageLimits"); rust/crates/sky/tests/ai_gcp_live_ledger.rs checks that each
worst case covers its window at the catalogue's rates and every measured run.

`append` builds one ledger line from the stage log (what each stage ran, with
its estimate) and the measurement log (the JSON lines the scenarios printed,
for example l5's cold start). Only numbers, the `ok` of each outcome and the
length of each list are kept from a measurement: response texts and error
strings stay out (an error can name a URL or a project).
"""
import argparse
import datetime
import json
import sys


def load_prices(path):
    with open(path) as f:
        return json.load(f)


def cmd_worst(args):
    worst = load_prices(args.prices)["stageWorstCaseUsd"]
    print("%.2f" % sum(float(worst.get(s, 0)) for s in args.stages.split(",")))


def cmd_guard(args):
    total = 0.0
    with open(args.ledger) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            total += float(r["actualUsd"] if r.get("actualUsd") is not None else r["worstCaseUsd"])
    need = total + 1.25 * float(args.worst)
    print("gcp-live: ledger total USD %.2f, this run's worst case USD %s, guard %.2f of %s"
          % (total, args.worst, need, args.cap))
    if need > float(args.cap):
        print("gcp-live: REFUSED: the ledger has no room for this run under the cap", file=sys.stderr)
        sys.exit(4)


def cmd_stage_seconds(args):
    limits = load_prices(args.prices)["stageLimits"]
    print(int(limits["stageSeconds"][args.stage]))


def cmd_cancel_wait(args):
    print(int(load_prices(args.prices)["stageLimits"]["cancelWaitSeconds"]))


def measured_figures(line):
    """The figures worth keeping from one scenario line."""
    out = {}
    for key, value in sorted(line.items()):
        if key == "scenario":
            continue
        if isinstance(value, bool):
            out[key] = value
        elif isinstance(value, (int, float)):
            out[key] = value
        elif isinstance(value, dict) and isinstance(value.get("ok"), bool):
            out[key + "Ok"] = value["ok"]
        elif isinstance(value, list):
            # hotLoadErrors -> hotLoadErrorCount
            stem = key[:-1] if key.endswith("s") else key
            out[stem + "Count"] = len(value)
    return out


def measurements(path):
    """Scenario lines in the measurement log, keyed by stage (l5 for l5)."""
    found = {}
    try:
        f = open(path)
    except FileNotFoundError:
        return found
    with f:
        for raw in f:
            raw = raw.strip()
            if not raw:
                continue
            line = json.loads(raw)
            scenario = line.get("scenario")
            if isinstance(scenario, str) and scenario:
                found[scenario] = measured_figures(line)
    return found


def cmd_append(args):
    with open(args.stage_log) as f:
        details = [json.loads(l) for l in f if l.strip()]
    estimate = round(sum(float(d.get("estimateUsd", 0)) for d in details), 4)
    line = {"run": args.run, "utc": args.started,
            "ended": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "gitSha": args.sha, "project": args.project_tag, "stages": args.stages.split(","),
            "resources": details, "estimateUsd": estimate, "worstCaseUsd": float(args.worst),
            "leakCheck": args.leak, "testsPassed": args.status == "0", "actualUsd": None}
    m = measurements(args.measure_log) if args.measure_log else {}
    if m:
        line["measurements"] = m
    with open(args.ledger, "a") as f:
        f.write(json.dumps(line, sort_keys=True) + "\n")
    print("gcp-live: ledger: estimate USD %.4f (worst case USD %s), leak check %s"
          % (estimate, args.worst, args.leak))
    for stage, figures in sorted(m.items()):
        print("gcp-live: ledger: %s measured %s" % (stage, json.dumps(figures, sort_keys=True)))


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    w = sub.add_parser("worst")
    w.add_argument("prices")
    w.add_argument("stages")
    w.set_defaults(fn=cmd_worst)
    g = sub.add_parser("guard")
    g.add_argument("ledger")
    g.add_argument("worst")
    g.add_argument("cap")
    g.set_defaults(fn=cmd_guard)
    s = sub.add_parser("stage-seconds")
    s.add_argument("prices")
    s.add_argument("stage")
    s.set_defaults(fn=cmd_stage_seconds)
    c = sub.add_parser("cancel-wait")
    c.add_argument("prices")
    c.set_defaults(fn=cmd_cancel_wait)
    a = sub.add_parser("append")
    for flag in ("--ledger", "--stage-log", "--run", "--started", "--stages", "--worst",
                 "--leak", "--project-tag", "--status", "--sha"):
        a.add_argument(flag, required=True)
    a.add_argument("--measure-log", default="")
    a.set_defaults(fn=cmd_append)
    args = p.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
