"""The Fake backend: a scripted trainer for tests and for machines with no
accelerator. Standard library only.

It runs the whole protocol (steps, evaluations, checkpoints, resume, cancel)
with a loss curve that is a fixed function of the step and the seed, so a
resumed run reports the same numbers as an uninterrupted one. Its adapter is a
real PEFT LoRA adapter for the serving base's shapes (driver.json "shapes"):
rank `rank`, small seeded A matrices and zero B matrices. B = 0 makes it a
no-op, so it converts to GGUF and loads in llama-server without changing the
model's answers.
"""

import math
import os
import platform
import random
import time

import sky_protocol as P


def target_dims(target, shapes):
    """(in, out) of one projection, from the base model's shapes."""
    h = shapes["hidden"]
    q = shapes.get("qDim", h)
    kv = shapes.get("kvDim", h)
    ff = shapes["intermediate"]
    name = target.split(".")[-1]
    return {
        "q_proj": (h, q),
        "k_proj": (h, kv),
        "v_proj": (h, kv),
        "o_proj": (q, h),
        "gate_proj": (h, ff),
        "up_proj": (h, ff),
        "down_proj": (ff, h),
    }[name]


def lora_layers(cfg):
    n = cfg["shapes"]["layers"]
    want = cfg.get("layers", 16)
    if want < 0 or want > n:
        want = n
    return list(range(n - want, n))


class FakeBackend(object):
    def __init__(self):
        self.k = 0
        self.m = 0.0

    def versions(self):
        return {"python": platform.python_version(), "fake": "1"}

    def prepare(self, cfg, train, valid):
        self.cfg = cfg
        self.train = train
        self.valid = valid
        self.total = P.total_steps(cfg, len(train))
        self.step_ms = int(cfg.get("fakeStepMs", 20))

    def loss_at(self, k):
        seed = int(self.cfg["seed"])
        frac = float(k) / max(1, self.total)
        return 0.45 + 1.6 * math.exp(-3.0 * frac) + 0.02 * math.sin(k * 0.7 + seed)

    def train_step(self, indices, accumulate):
        if self.step_ms > 0:
            time.sleep(self.step_ms / 1000.0)
        self.k += 1
        self.m = 0.9 * self.m + 0.1 * self.loss_at(self.k)
        return self.loss_at(self.k), 32 * len(indices)

    def evaluate(self):
        return self.loss_at(self.k) + 0.08

    def save(self, d):
        P.write_safetensors(
            os.path.join(d, "adapter.safetensors"),
            {"fake.step": ("F32", (1,), P.f32_bytes([float(self.k)]))},
        )
        P.write_safetensors(
            os.path.join(d, "optimizer.safetensors"),
            {"fake.m": ("F32", (1,), P.f32_bytes([self.m]))},
        )

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
                key = "model.layers.%d.%s" % (layer, target)
                a = [rng.uniform(-0.01, 0.01) for _ in range(rank * din)]
                tensors[P.mlx_to_peft_name(key + ".lora_a")] = ("F32", (rank, din), P.f32_bytes(a))
                tensors[P.mlx_to_peft_name(key + ".lora_b")] = ("F32", (dout, rank), b"\x00" * (4 * dout * rank))
        P.write_safetensors(os.path.join(out, "adapter_model.safetensors"), tensors, {"format": "pt"})
        P.write_json(
            os.path.join(out, "adapter_config.json"),
            P.peft_config(cfg["base"]["id"], rank, int(cfg["alpha"]), cfg["targets"]),
        )
