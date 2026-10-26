#!/usr/bin/env bash
#
# scripts/ai/trainer/test-trainer.sh - the native trainer's contract, on the
# synthetic tiny Qwen2 model (`go run ./cmd/sky-tiny-gguf`, its Hugging Face
# twin), never a real model:
#
#   1. --selftest: the LoRA gradient matches central finite differences
#      (dense, quantised, tied, untied, with and without checkpointing).
#   2. The training loss falls.
#   3. A run killed after its step-4 checkpoint and resumed from it ends with
#      the same adapter, byte for byte, as a run that was never killed (CPU).
#   4. Gradient checkpointing does not change the adapter (CPU).
#   5. A cancel writes a verified checkpoint at the step it stopped at.
#   6. A memory cap below the trainer's footprint: the guard kills it and
#      reports ModelTooLarge (guard.json and the error line).
#   7. An MLX-quantised base and a base quantised on load both train.
#   8. The Go converter turns the adapter into a GGUF LoRA for the GGUF model.
#   9. The command line the runtime launches an attempt with (driver.json ->
#      argv, launch token included) trains, and resumes from a checkpoint.
#
#   test-trainer.sh (--trainer <sky-trainer> | --mlx <prefix> --mlx-c <src>)
#                   [--device cpu|gpu|both] [--work <dir>]
#
# Every trainer run passes --memory-cap (2.5 GB unless the test lowers it),
# and an independent watchdog here SIGKILLs any run above
# SKY_TRAINER_TEST_KILL_BYTES (3 GB) or past its deadline. Each run's peak
# footprint is printed.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool go "install Go 1.25+"

TRAINER="" MLX="" MLXC="" DEVICE="both" WORK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --trainer) TRAINER="$2"; shift 2 ;;
    --mlx) MLX="$2"; shift 2 ;;
    --mlx-c) MLXC="$2"; shift 2 ;;
    --device) DEVICE="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    *) echo "usage: test-trainer.sh (--trainer <bin> | --mlx <prefix> --mlx-c <src>) [--device cpu|gpu|both] [--work <dir>]" >&2; exit 2 ;;
  esac
done
case "$DEVICE" in cpu|gpu|both) ;; *) echo "test-trainer: --device is cpu, gpu or both" >&2; exit 2 ;; esac
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/sky-trainer-test.XXXXXX")}"
mkdir -p "$WORK"
if [ -z "$TRAINER" ]; then
  [ -n "$MLX" ] && [ -n "$MLXC" ] || { echo "test-trainer: give --trainer, or --mlx and --mlx-c to build one" >&2; exit 2; }
  TRAINER="$("$HERE/build.sh" --mlx "$MLX" --mlx-c "$MLXC" --out "$WORK/build" | tail -1)"
fi
[ -x "$TRAINER" ] || { echo "test-trainer: $TRAINER is not an executable" >&2; exit 1; }

CAP=2500000000
KILL_BYTES="${SKY_TRAINER_TEST_KILL_BYTES:-3000000000}"
FAILS=0
fail() { echo "FAIL: $*" >&2; FAILS=$((FAILS + 1)); }
pass() { echo "ok    $*"; }

footprint() {
  if [ "$(uname -s)" = Darwin ]; then
    { /usr/bin/footprint -f bytes --noCategories "$1" 2>/dev/null || true; } | sed -n 's/.*Footprint: \([0-9][0-9]*\) B.*/\1/p' | head -1
  else
    awk '/^VmRSS:/ {print $2 * 1024}' "/proc/$1/status" 2>/dev/null || true
  fi
}

# run <name> <deadline-seconds> <trainer flags...>: runs the trainer with its
# stdout in <name>.events, under the watchdog. Sets RC and PEAK.
run() {
  local name="$1" deadline="$2"; shift 2
  "$TRAINER" "$@" > "$WORK/$name.events" 2> "$WORK/$name.err" &
  local pid=$! peak=0 t0=$SECONDS fp
  while kill -0 "$pid" 2>/dev/null; do
    fp="$(footprint "$pid" || true)"
    if [ -n "$fp" ]; then
      [ "$fp" -gt "$peak" ] && peak=$fp
      if [ "$fp" -gt "$KILL_BYTES" ]; then
        kill -9 "$pid" 2>/dev/null
        echo "test-trainer: watchdog killed $name at $fp bytes" >&2
      fi
    fi
    if [ $((SECONDS - t0)) -gt "$deadline" ]; then
      kill -9 "$pid" 2>/dev/null
      echo "test-trainer: watchdog killed $name after ${deadline}s" >&2
    fi
    sleep 0.1
  done
  set +e
  wait "$pid"
  RC=$?
  set -e
  PEAK=$peak
  echo "      $name: exit $RC, peak footprint $PEAK bytes"
}

first_loss() { grep '"type":"progress"' "$1" | head -1 | sed -n 's/.*"loss":\([^,]*\),.*/\1/p'; }

# ─── the model and the data ─────────────────────────────────────────────
TINY="$(cd "$ROOT/runtime-go" && with_timeout 600 go run ./cmd/sky-tiny-gguf "$WORK/model")"
GGUF="$(echo "$TINY" | sed -n 's/.*"model":"\([^"]*\)".*/\1/p')"
HF="$(echo "$TINY" | sed -n 's/.*"hf":"\([^"]*\)".*/\1/p')"
[ -f "$GGUF" ] && [ -f "$HF/model.safetensors" ] || { echo "test-trainer: sky-tiny-gguf wrote no model ($TINY)" >&2; exit 1; }
SHAPE=(--model "$HF" --layers 2 --hidden 64 --heads 4 --kv-heads 2 --intermediate 128 --vocab 259 --tied 0
  --rope-theta 1000000 --rms-eps 0.000001 --base-id sky-tiny-qwen2)
( cd "$ROOT/runtime-go" && with_timeout 600 go build -o "$WORK/sky-train-tool" ./cmd/sky-train-tool )

# stage <run> <maxSteps> <perDevice> <accumulate>: a staged, tokenized run.
stage() {
  local run="$1" steps="$2" per="$3" acc="$4"
  rm -rf "$run"
  mkdir -p "$run/data"
  : > "$run/data/train.jsonl"
  local words=(apple brick cloud delta ember frost grape harbor island jungle kettle lemon)
  for i in $(seq 0 23); do
    local w="${words[$((i % 12))]}"
    printf '{"messages":[{"role":"user","content":"echo %s"},{"role":"assistant","content":"%s %s"}]}\n' "$w" "$w" "$w" >> "$run/data/train.jsonl"
  done
  : > "$run/data/valid.jsonl"
  for w in apple frost lemon cloud; do
    printf '{"messages":[{"role":"user","content":"echo %s"},{"role":"assistant","content":"%s %s"}]}\n' "$w" "$w" "$w" >> "$run/data/valid.jsonl"
  done
  printf '{"batch":{"accumulate":%d,"perDevice":%d},"epochs":1,"format":"chat","maxSteps":%d,"seed":7,"sequenceLength":64}\n' \
    "$acc" "$per" "$steps" > "$run/job.json"
  with_timeout 300 "$WORK/sky-train-tool" tokenize "$run" "$HF" > /dev/null
}

LORA=(--rank 4 --alpha 8 --lora-layers 2 --targets q_proj,k_proj,v_proj,o_proj,gate_proj,up_proj,down_proj --seed 7)

devices=()
case "$DEVICE" in cpu) devices=(cpu) ;; gpu) devices=(gpu) ;; both) devices=(cpu gpu) ;; esac

for dev in "${devices[@]}"; do
  D=()
  [ "$dev" = cpu ] && D=(--cpu)
  echo "== device $dev"

  # 1. selftest
  run "selftest-$dev" 300 --selftest ${D[@]+"${D[@]}"} --memory-cap "$CAP"
  if [ "$RC" = 0 ] && grep -q '^selftest ok' "$WORK/selftest-$dev.events"; then pass "selftest ($dev)"; else fail "selftest ($dev): $(tail -5 "$WORK/selftest-$dev.err")"; fi

  # 2. the loss falls
  R="$WORK/learn-$dev"
  stage "$R" 60 2 1
  run "learn-$dev" 600 --run "$R" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.01 --batch 2 --accumulate 1 \
    --checkpoint-every 0 --eval-every 20 --memory-cap "$CAP"
  # The tiny model's lm_head is random with std 0.02 over 64 dimensions, so
  # no adapter can move its logits far from uniform (ln 259 = 5.56): the
  # loss can only fall by about half a nat. The final eval loss must sit at
  # least 0.3 below the first step's loss (the base, as B starts at 0).
  f="$(first_loss "$WORK/learn-$dev.events")"
  l="$(sed -n 's/.*"type":"done".*"valLoss":\([0-9.]*\)}.*/\1/p' "$WORK/learn-$dev.events")"
  if [ "$RC" = 0 ] && [ -n "$l" ] && awk -v f="$f" -v l="$l" 'BEGIN{exit !(l + 0.3 < f)}'; then
    pass "loss falls ($dev): first step $f, final eval $l"
  else
    fail "loss falls ($dev): rc $RC, $f -> $l; $(tail -3 "$WORK/learn-$dev.err")"
  fi

  # 3. kill after the step-4 checkpoint, resume, same adapter
  A="$WORK/whole-$dev" B="$WORK/resumed-$dev"
  stage "$A" 8 2 2
  stage "$B" 8 2 2
  run "whole-$dev" 300 --run "$A" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 2 \
    --checkpoint-every 4 --eval-every 0 --memory-cap "$CAP"
  run "crashed-$dev" 300 --run "$B" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 2 \
    --checkpoint-every 4 --eval-every 0 --memory-cap "$CAP" --crash-at 4
  crash_rc=$RC
  run "resume-$dev" 300 --run "$B" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 2 \
    --checkpoint-every 4 --eval-every 0 --memory-cap "$CAP" --attempt 1 --resume-step 4 --resume-dir checkpoints/0000004
  if [ "$crash_rc" = 137 ] && [ "$RC" = 0 ] && [ -f "$B/checkpoints/0000004/manifest.json" ] \
    && grep -q '"resumeFrom":4' "$WORK/resume-$dev.events"; then
    if cmp -s "$A/adapter/adapter_model.safetensors" "$B/adapter/adapter_model.safetensors"; then
      pass "kill at step 4 + resume = the same adapter ($dev)"
    elif [ "$dev" = cpu ]; then
      fail "resume (cpu): the resumed adapter differs from the whole run's"
    else
      echo "note  resume ($dev): the resumed adapter is not byte-identical (GPU kernels may reorder sums)"
    fi
  else
    fail "resume ($dev): crash rc $crash_rc, resume rc $RC; $(tail -3 "$WORK/resume-$dev.err")"
  fi

  # 4. gradient checkpointing gives the same adapter
  G="$WORK/ckpt-$dev"
  stage "$G" 8 2 2
  run "ckpt-$dev" 300 --run "$G" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 2 \
    --checkpoint-every 4 --eval-every 0 --memory-cap "$CAP" --grad-checkpoint 1
  if [ "$RC" = 0 ] && cmp -s "$A/adapter/adapter_model.safetensors" "$G/adapter/adapter_model.safetensors"; then
    pass "gradient checkpointing keeps the adapter ($dev)"
  elif [ "$RC" = 0 ] && [ "$dev" = gpu ]; then
    echo "note  gradient checkpointing ($dev): not byte-identical on the GPU"
  else
    fail "gradient checkpointing ($dev): rc $RC, adapters differ"
  fi

  # 5. cancel writes a verified checkpoint
  K="$WORK/cancel-$dev"
  stage "$K" 60 2 1
  (
    for _ in $(seq 1 300); do
      if grep -q '"step":3,' "$WORK/cancel-$dev.events" 2>/dev/null; then touch "$K/cancel"; exit 0; fi
      sleep 0.05
    done
  ) &
  canceller=$!
  run "cancel-$dev" 300 --run "$K" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 1 \
    --checkpoint-every 0 --eval-every 0 --memory-cap "$CAP" --pace-ms 100
  wait "$canceller" 2>/dev/null || true
  cstep="$(sed -n 's/.*"step":\([0-9]*\),"type":"cancelled".*/\1/p' "$WORK/cancel-$dev.events")"
  cdir="$K/checkpoints/$(printf '%07d' "${cstep:-0}")"
  ok=0
  if [ "$RC" = 0 ] && [ -n "$cstep" ] && [ "$cstep" -ge 3 ] && [ -f "$cdir/manifest.json" ]; then
    ok=1
    for f in adapter.safetensors optimizer.safetensors state.json; do
      want="$(sed -n "s/.*\"$f\":\"\([0-9a-f]*\)\".*/\1/p" "$cdir/manifest.json")"
      got="$(shasum -a 256 "$cdir/$f" | cut -d' ' -f1)"
      [ -n "$want" ] && [ "$want" = "$got" ] || ok=0
    done
  fi
  if [ "$ok" = 1 ]; then pass "cancel at step $cstep writes a verified checkpoint ($dev)"; else fail "cancel ($dev): rc $RC, step '${cstep}'"; fi

  # 6. the guard: a cap below the trainer's footprint ends it as ModelTooLarge
  M="$WORK/guard-$dev"
  stage "$M" 60 2 1
  run "guard-$dev" 120 --run "$M" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 1 \
    --checkpoint-every 0 --eval-every 0 --memory-cap 5000000 --pace-ms 100
  if [ "$RC" = 137 ] && grep -q '"code":"ModelTooLarge"' "$M/guard.json" 2>/dev/null \
    && tail -1 "$WORK/guard-$dev.events" | grep -q '"code":"ModelTooLarge".*"type":"error"'; then
    pass "a 5 MB cap: the guard kills the trainer with ModelTooLarge ($dev)"
  else
    fail "guard ($dev): rc $RC, guard.json $(cat "$M/guard.json" 2>/dev/null || echo absent)"
  fi

  # 7. quantised bases
  Q="$WORK/q4-$dev"
  stage "$Q" 8 2 1
  run "q4load-$dev" 300 --run "$Q" "${SHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 1 \
    --checkpoint-every 0 --eval-every 0 --memory-cap "$CAP" --quant-bits 4
  q4load_rc=$RC
  QB="$WORK/model-q4"
  if [ ! -f "$QB/model.safetensors" ]; then
    run "quantise" 120 "${SHAPE[@]}" --cpu --quantise-snapshot "$QB" --quant-bits 4 --memory-cap "$CAP"
    /bin/cp -f "$HF/config.json" "$HF/tokenizer.json" "$HF/tokenizer_config.json" "$QB/"
  fi
  QSHAPE=("${SHAPE[@]}")
  QSHAPE[1]="$QB"
  Q2="$WORK/q4base-$dev"
  stage "$Q2" 8 2 1
  run "q4base-$dev" 300 --run "$Q2" "${QSHAPE[@]}" "${LORA[@]}" ${D[@]+"${D[@]}"} --lr 0.005 --batch 2 --accumulate 1 \
    --checkpoint-every 0 --eval-every 0 --memory-cap "$CAP" --base-bits 4 --base-group-size 64
  if [ "$q4load_rc" = 0 ] && [ "$RC" = 0 ] && grep -q '"type":"done"' "$WORK/q4base-$dev.events" \
    && ! grep -q '"loss":null' "$WORK/q4base-$dev.events"; then
    pass "a 4-bit base, quantised on load or by MLX, trains ($dev)"
  else
    fail "quantised bases ($dev): on load rc $q4load_rc, MLX-quantised rc $RC"
  fi

  # 8. the Go converter accepts the adapter for the GGUF model
  if with_timeout 120 "$WORK/sky-train-tool" convert "$A" "$GGUF" > "$WORK/convert-$dev.json" 2> "$WORK/convert-$dev.err" \
    && grep -q '"tensors":28' "$WORK/convert-$dev.json"; then
    pass "the adapter converts to a GGUF LoRA for the GGUF model ($dev)"
  else
    fail "convert ($dev): $(cat "$WORK/convert-$dev.err" "$WORK/convert-$dev.json" 2>/dev/null | tail -3)"
  fi

  # 9. the runtime's command line: driver.json -> argv -> a run, then a resume
  N="$WORK/argv-$dev"
  stage "$N" 8 2 1
  device=""
  [ "$dev" = cpu ] && device=',"device":"cpu"'
  for attempt in 0 1; do
    resume='"resumeFrom":0,"resumeDir":""'
    [ "$attempt" = 1 ] && resume='"resumeFrom":4,"resumeDir":"checkpoints/0000004"'
    printf '{"alpha":8,"attempt":%d,"backend":"mlx","base":{"id":"sky-tiny-qwen2","weights":"%s"},"batch":{"accumulate":1,"perDevice":2},"checkpointEvery":4,"evalEvery":0,"layers":-1,"learningRate":0.005,"quantised":false,"rank":4,%s,"seed":7,"targets":["self_attn.q_proj","self_attn.v_proj","mlp.down_proj"],"trainer":"%s","v":1%s}\n' \
      "$attempt" "$HF" "$resume" "$TRAINER" "$device" > "$N/driver.json"
    ARGV=()
    while IFS= read -r a; do ARGV+=("$a"); done < <("$WORK/sky-train-tool" argv "$N" "$N/driver.json")
    crash=()
    [ "$attempt" = 0 ] && crash=(--crash-at 4)
    run "argv$attempt-$dev" 300 "${ARGV[@]}" --memory-cap "$CAP" ${crash[@]+"${crash[@]}"}
    eval "argv_rc$attempt=$RC"
  done
  if [ "${#ARGV[@]}" -gt 0 ] && [ "$argv_rc0" = 137 ] && [ "$argv_rc1" = 0 ] \
    && grep -q '"resumeFrom":4' "$WORK/argv1-$dev.events" && grep -q '"type":"done"' "$WORK/argv1-$dev.events" \
    && printf '%s\n' "${ARGV[@]}" | grep -qx -- --launch; then
    pass "the runtime's command line trains and resumes ($dev)"
  else
    fail "argv ($dev): first rc ${argv_rc0:-?}, resume rc ${argv_rc1:-?}; $(tail -3 "$WORK/argv1-$dev.err" 2>/dev/null)"
  fi
done

if [ "$FAILS" -gt 0 ]; then
  echo "test-trainer: $FAILS FAILED (work dir $WORK)" >&2
  exit 1
fi
echo "test-trainer: all passed (work dir $WORK)"
