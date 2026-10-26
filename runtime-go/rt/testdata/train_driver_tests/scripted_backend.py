"""A scripted backend for the driver's own tests: no GPU, no ML library.

The driver (sky_train.py) now runs the Unsloth backend only; the Fake backend
trains in the Sky runtime itself (runtime-go/rt/ai_fake_trainer.go). These
tests still need a backend to drive the driver's loop (steps, evaluations,
checkpoints, resume, cancel, a crash), so this one lives with them. Its loss
is a fixed function of the step and the seed, so a resumed run reports the
same numbers as an uninterrupted one, and its adapter is a PEFT LoRA adapter
with zero B matrices.
"""

import math
import os
import random
import sys
import time

import sky_protocol as P


def target_dims(target, shapes):
    """(in, out) of one projection, from the base model's shapes."""
    h = shapes["hidden"]
    q = shapes.get("qDim", h)
    kv = shapes.get("kvDim", h)
    ff = shapes["intermediate"]
    return {
        "q_proj": (h, q),
        "k_proj": (h, kv),
        "v_proj": (h, kv),
        "o_proj": (q, h),
        "gate_proj": (h, ff),
        "up_proj": (h, ff),
        "down_proj": (ff, h),
    }[target.split(".")[-1]]


def lora_layers(cfg):
    n = cfg["shapes"]["layers"]
    want = cfg.get("layers", 16)
    if want < 0 or want > n:
        want = n
    return list(range(n - want, n))


class ScriptedBackend(object):
    def __init__(self):
        self.k = 0
        self.m = 0.0

    def versions(self):
        return {"scripted": "1"}

    def prepare(self, cfg, train, valid):
        self.cfg = cfg
        self.total = P.total_steps(cfg, len(train))
        self.step_ms = int(cfg.get("fakeStepMs", 20))

    def loss_at(self, k):
        frac = float(k) / max(1, self.total)
        return 0.45 + 1.6 * math.exp(-3.0 * frac) + 0.02 * math.sin(k * 0.7 + int(self.cfg["seed"]))

    def train_step(self, indices, accumulate):
        if self.step_ms > 0:
            time.sleep(self.step_ms / 1000.0)
        self.k += 1
        self.m = 0.9 * self.m + 0.1 * self.loss_at(self.k)
        return self.loss_at(self.k), 32 * len(indices)

    def evaluate(self):
        return self.loss_at(self.k) + 0.08

    def save(self, d):
        P.write_safetensors(os.path.join(d, "adapter.safetensors"), {"fake.step": ("F32", (1,), P.f32_bytes([float(self.k)]))})
        P.write_safetensors(os.path.join(d, "optimizer.safetensors"), {"fake.m": ("F32", (1,), P.f32_bytes([self.m]))})

    def load(self, d):
        adapter, _ = P.read_safetensors(os.path.join(d, "adapter.safetensors"))
        opt, _ = P.read_safetensors(os.path.join(d, "optimizer.safetensors"))
        self.k = int(P.f32_values(adapter["fake.step"][2])[0])
        self.m = P.f32_values(opt["fake.m"][2])[0]

    def export_peft(self, out):
        cfg = self.cfg
        rank = int(cfg["rank"])
        rng = random.Random(int(cfg["seed"]))
        tensors = {}
        for layer in lora_layers(cfg):
            for target in cfg["targets"]:
                din, dout = target_dims(target, cfg["shapes"])
                base = "base_model.model.model.layers.%d.%s" % (layer, target)
                a = [rng.uniform(-0.01, 0.01) for _ in range(rank * din)]
                tensors[base + ".lora_A.weight"] = ("F32", (rank, din), P.f32_bytes(a))
                tensors[base + ".lora_B.weight"] = ("F32", (dout, rank), b"\x00" * (4 * dout * rank))
        P.write_safetensors(os.path.join(out, "adapter_model.safetensors"), tensors, {"format": "pt"})
        P.write_json(os.path.join(out, "adapter_config.json"), P.peft_config(cfg["base"]["id"], rank, int(cfg["alpha"]), cfg["targets"]))


def driver_argv(driver_dir, tests_dir):
    """The driver's main() with this backend in place of the one driver.json
    names, as an argv prefix: the driver's --run and --launch follow it. A
    separate isolated process, as the Go runtime and the cloud shim start the
    driver."""
    code = (
        "import signal, sys; sys.path[:0] = [%r, %r]; "
        "import sky_protocol as P, sky_train, scripted_backend; "
        "P.claim_stdout(); signal.signal(signal.SIGTERM, sky_train._on_term); "
        "sys.exit(sky_train.run(sky_train.parse_args(sys.argv[1:])['run'], backend=scripted_backend.ScriptedBackend()))"
    ) % (driver_dir, tests_dir)
    return [sys.executable, "-I", "-B", "-c", code]
