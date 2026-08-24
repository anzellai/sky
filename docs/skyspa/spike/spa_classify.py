#!/usr/bin/env python3
"""
Sky.Spa auto-split feasibility classifier.

Not sound. Credibly COUNTS the syntactic collapse-triggers the Sky.Spa thesis
(docs/skyspa/design.md §4/§4.1) depends on, per real `update` function.

Triggers counted per top-level `case msg of` arm (and distinct nested sub-arms
returning their own (Model, Cmd) tuple):

  MIXED-BATCH        : returned Cmd is `Cmd.batch [...]`  (>1 effect target)
  REAL-CMD-EFFECT    : returned Cmd is `Cmd.perform`/carries a Task (visible effect)
  CMD-NONE           : returned Cmd is `Cmd.none`
  IMPRECISE-WRITE    : model expr is a bare helper call `foo ... model` /
                       `let ...=helper...` — write-set not syntactically visible
  RECORD-UPDATE      : model expr is a literal `{ model | f = .. }` (precise);
                       field names extracted
  SYNC-SERVER-IN-MODEL: a Db./Store./Task.run/<Lib>.<verb> server op runs inside
                       the model position (arm body or the helper it calls) while
                       the Cmd is none -> effect invisible to a Cmd-based classifier

The point: the thesis classifies branches by the RETURNED Cmd's effect target.
This script shows how often real Sky code (a) hides the effect in the model
position under Cmd.none, (b) hands the write-set to a helper, (c) batches
targets — each of which defeats the per-branch one-target-one-RPC partition.
"""
import re, sys, os

ROOT = "/Users/anzel/works/playground/sky"

# (app, file, update-fn start line 1-based, end line or None=EOF-ish)
TARGETS = [
    ("09-live-counter",       "examples/09-live-counter/src/Main.sky", None),
    ("19-skyforum",           "examples/19-skyforum/src/Update.sky",   None),
    ("13-skyshop",            "examples/13-skyshop/src/Main.sky",      None),
    ("12-skyvote",            "examples/12-skyvote/src/Main.sky",      None),
    ("52-blog-analytics",     "examples/52-blog-analytics/src/Main.sky", None),
    ("37-composite-live-shop","examples/37-composite-live-shop/src/Update.sky", None),
    ("27-multi-session-chat", "examples/27-multi-session-chat/src/Main.sky", None),
    ("18-job-queue",          "examples/18-job-queue/src/Main.sky",    None),
]

SERVER_OP = re.compile(r'\b(Db\.\w+|Store\.\w+|Task\.run|Auth\.(?:sign|verify|getSessionUser)'
                       r'|Products\.\w+|Cart\.\w+|Ideas\.\w+|Comments\.\w+|Analytics\.\w+'
                       r'|persistMessage|loadRoomHistory|loadHistory|saveSnapshot'
                       r'|refresh\w+|load\w+|withDashboard)\b')

def extract_update(lines):
    # find `update msg model =` (the fn body), return (start_idx, end_idx)
    start = None
    for i, l in enumerate(lines):
        if re.match(r'^update\s+\w+\s+\w+\s*=', l) or re.match(r'^update msg model =', l):
            start = i
            break
    if start is None:
        # signature-only line then body
        for i, l in enumerate(lines):
            if re.match(r'^update\s*:', l):
                start = i
                break
    if start is None:
        return None
    # end = next top-level decl (col 0, not comment/blank) after the body starts
    end = len(lines)
    for j in range(start+2, len(lines)):
        l = lines[j]
        if l and not l[0].isspace() and not l.startswith('--'):
            end = j
            break
    return start, end

def split_arms(block):
    """Top-level case arms live at 8-space indent: `        Ctor ... ->`.
    Also capture distinct nested sub-arms at 16-space indent that end in `->`
    and return their own tuple. Returns list of (label, indent, body_lines)."""
    arms = []
    cur = None
    for l in block:
        m8 = re.match(r'^        ([A-Z]\w*)(.*)->\s*$', l)          # top-level arm
        m16 = re.match(r'^                (Nothing|Just\b.*|Ok\b.*|Err\b.*|if\b.*|else\b.*)->\s*$', l)
        if m8:
            if cur: arms.append(cur)
            cur = [m8.group(1), 8, []]
        elif m16 and cur:
            # start a nested sub-branch as its own arm (best-effort)
            if cur: arms.append(cur)
            lbl = cur[0] + " / " + m16.group(1).strip()
            cur = [lbl, 16, []]
        elif cur is not None:
            cur[2].append(l)
    if cur: arms.append(cur)
    return arms

def classify(app, path):
    full = open(os.path.join(ROOT, path)).read().split('\n')
    rng = extract_update(full)
    if rng is None:
        return None
    s, e = rng
    block = full[s:e]
    fulltext = "\n".join(full)   # whole file, to peek into helper bodies
    arms = split_arms(block)
    stats = dict(arms=0, cmd_none=0, cmd_batch=0, real_effect=0,
                 record_update=0, imprecise=0, sync_server=0, clean_syntactic=0)
    rows = []
    for label, indent, body in arms:
        bt = "\n".join(body)
        if not bt.strip():
            continue
        stats['arms'] += 1
        cmd_batch = 'Cmd.batch' in bt
        real_effect = ('Cmd.perform' in bt) or ('Cmd.task' in bt)
        cmd_none = ('Cmd.none' in bt) and not cmd_batch and not real_effect
        record_update = bool(re.search(r'\{\s*model\s*\|', bt))
        # helper-call model expr: `Ctor -> foo ... model` with no `{model|` and
        # first token after -> is a lowercase identifier call
        first = next((x.strip() for x in body if x.strip()), "")
        helper_ret = bool(re.match(r'^[a-z]\w*(\.\w+)?\s', first)) and not record_update and '(' + 'model' not in first
        imprecise = (not record_update) and (
            re.search(r'^\s*[a-z]\w*(\.\w+)?\s+.*\bmodel\b', first) is not None
            or 'let' in first)
        # sync server op inside arm body OR inside a helper it names
        sync = bool(SERVER_OP.search(bt))
        if not sync:
            # peek: does it call a helper whose body touches a server op?
            for hm in re.findall(r'\b([a-z]\w+)\b', bt):
                hb = re.search(r'^'+re.escape(hm)+r'\b.*=\n((?:[ \t].*\n)+)', fulltext, re.M)
                if hb and SERVER_OP.search(hb.group(1)):
                    sync = True; break
        if cmd_batch: stats['cmd_batch'] += 1
        if real_effect: stats['real_effect'] += 1
        if cmd_none: stats['cmd_none'] += 1
        if record_update: stats['record_update'] += 1
        if imprecise: stats['imprecise'] += 1
        if sync and cmd_none: stats['sync_server'] += 1
        # syntactic-clean = single visible non-batch target AND precise write AND
        # no hidden server op under Cmd.none
        syntactic_clean = (record_update and not cmd_batch and not imprecise
                           and not (sync and cmd_none))
        if syntactic_clean: stats['clean_syntactic'] += 1
        trig = []
        if cmd_batch: trig.append('MIXED-BATCH')
        if imprecise: trig.append('IMPRECISE-WRITE')
        if sync and cmd_none: trig.append('SYNC-SERVER-IN-MODEL(Cmd.none)')
        rows.append((label, 'batch' if cmd_batch else 'perform' if real_effect else 'none',
                     'helper' if imprecise else 'record' if record_update else '?',
                     'srv' if sync else '-', ','.join(trig) or 'clean-syntactic'))
    return stats, rows

def main():
    tot = dict(arms=0, cmd_none=0, cmd_batch=0, real_effect=0,
               record_update=0, imprecise=0, sync_server=0, clean_syntactic=0)
    for app, path, _ in TARGETS:
        r = classify(app, path)
        if r is None:
            print(f"\n### {app}: no `update` fn (not TEA) — SKIP"); continue
        stats, rows = r
        print(f"\n### {app}  ({path})")
        for k in tot: tot[k]+=stats[k]
        print(f"    arms={stats['arms']}  Cmd.none={stats['cmd_none']}  "
              f"Cmd.batch={stats['cmd_batch']}  Cmd.perform={stats['real_effect']}  "
              f"record-update={stats['record_update']}  imprecise={stats['imprecise']}  "
              f"sync-server-under-none={stats['sync_server']}  "
              f"syntactic-clean={stats['clean_syntactic']}")
        for lbl, cmd, wr, srv, trig in rows:
            print(f"      - {lbl:<34} cmd={cmd:<8} write={wr:<7} eff={srv:<4} {trig}")
    print("\n==================== TOTALS ====================")
    for k,v in tot.items(): print(f"  {k:<22} {v}")
    a = tot['arms']
    print(f"\n  syntactic-clean fraction : {tot['clean_syntactic']}/{a} = "
          f"{100*tot['clean_syntactic']/a:.0f}%")
    print(f"  dominant trigger         : "
          f"IMPRECISE-WRITE={tot['imprecise']}  "
          f"SYNC-SERVER-UNDER-none={tot['sync_server']}  "
          f"MIXED-BATCH={tot['cmd_batch']}")

if __name__ == '__main__':
    main()
