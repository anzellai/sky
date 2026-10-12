"""The Unsloth backend without a GPU: a recording fake stands in for torch and
Unsloth (the `api` object), and the test checks what the backend asks of it,
the protocol it writes, the safetensors checkpoint and resume, and the PEFT
export. The nightly `ai-unsloth-toolchain` job installs the real locked
toolchain on Linux; this file covers the driver's side of the contract.
"""

import json
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "..", "ai_train_assets", "driver")))

import sky_protocol as P  # noqa: E402
import sky_train  # noqa: E402
from backends import unsloth  # noqa: E402
from test_train_driver import events, job, make_run  # noqa: E402


class Tok(object):
    pad_token_id = 0

    def apply_chat_template(self, msgs, tokenize=True, add_generation_prompt=False):
        ids = []
        for m in msgs:
            ids += [1] + [3] * len(m["content"]) + [2]
        return ids + ([9] if add_generation_prompt else [])


class FakeApi(object):
    """Records every call; holds 'tensors' as plain float lists."""

    def __init__(self):
        self.calls = []
        self.weights = {}
        self.opt_state = {}
        self.k = 0

    def versions(self):
        return {"torch": "fake", "unsloth": "fake"}

    def load(self, weights, seq_len, load_in_4bit):
        self.calls.append(("load", weights, seq_len, load_in_4bit))
        return "base-model", Tok()

    def peft(self, model, rank, alpha, targets, seed, layers):
        self.calls.append(("peft", model, rank, alpha, sorted(targets), seed, layers))
        for layer in layers or []:
            for t in targets:
                pre = "base_model.model.model.layers.%d.%s" % (layer, t)
                self.weights[pre + ".lora_A.weight"] = [0.5] * (rank * 8)
                self.weights[pre + ".lora_B.weight"] = [0.0] * (8 * rank)
        self.rank = rank
        return "peft-model"

    def optimizer(self, model, lr):
        self.calls.append(("optimizer", model, lr))
        return "adamw"

    def step(self, model, opt, batches):
        self.k += 1
        self.calls.append(("step", len(batches), [len(ids) for ids, _ in batches]))
        for _, labels in batches:
            for row in labels:
                assert -100 in row  # the prompt is masked
        for name in self.weights:
            if name.endswith("lora_B.weight"):
                self.weights[name] = [v + 0.25 for v in self.weights[name]]
        self.opt_state["state.0.exp_avg"] = [float(self.k)]
        return 1.0 / self.k

    def eval_loss(self, model, ids, labels):
        return 0.5

    def lora_tensors(self, model):
        out = {}
        for name, vals in self.weights.items():
            shape = (self.rank, 8) if "lora_A" in name else (8, self.rank)
            out[name] = (shape, P.f32_bytes(vals))
        return out

    def load_lora_tensors(self, model, tensors):
        self.calls.append(("load_lora", len(tensors)))
        for name, (shape, data) in tensors.items():
            self.weights[name] = P.f32_values(data)

    def optimizer_tensors(self, opt):
        return {k: ((len(v),), P.f32_bytes(v)) for k, v in self.opt_state.items()}, json.dumps([{"lr": 0.0002}])

    def load_optimizer_tensors(self, opt, tensors, groups):
        self.calls.append(("load_optimizer", sorted(tensors), json.loads(groups)))
        self.opt_state = {k: P.f32_values(d) for k, (s, d) in tensors.items()}
        self.k = int(self.opt_state["state.0.exp_avg"][0])


def unsloth_job(**over):
    cfg = job(backend="unsloth", quantised=True, layers=2, targets=["self_attn.q_proj", "self_attn.v_proj"])
    cfg["base"] = {"id": "unsloth/Qwen2.5-0.5B-Instruct-bnb-4bit@abc", "weights": "/bases/q"}
    cfg["batch"] = {"perDevice": 2, "accumulate": 2}
    cfg.update(over)
    return cfg


class UnslothFixtureTest(unittest.TestCase):
    def capture(self, run_dir, backend):
        out = open(os.path.join(run_dir, "events.jsonl"), "a")
        old = sys.stdout
        sys.stdout = out
        try:
            return sky_train.run(run_dir, backend=backend)
        finally:
            sys.stdout = old
            out.close()

    def test_calls_protocol_checkpoints_and_export(self):
        d = make_run(unsloth_job())
        try:
            api = FakeApi()
            self.assertEqual(self.capture(d, unsloth.UnslothBackend(api)), 0)
            self.assertEqual(api.calls[0], ("load", "/bases/q", 64, True))
            self.assertEqual(api.calls[1], ("peft", "base-model", 2, 4, ["self_attn.q_proj", "self_attn.v_proj"], 7, [2, 3]))
            self.assertEqual(api.calls[2], ("optimizer", "peft-model", 0.0001))
            steps = [c for c in api.calls if c[0] == "step"]
            # 6 optimiser steps, each accumulating 2 micro-batches of 2 rows.
            self.assertEqual(len(steps), 6)
            self.assertEqual(steps[0][1:], (2, [2, 2]))
            evs = events(d)
            self.assertEqual([e["type"] for e in evs][-2:], ["artefact", "done"])
            self.assertEqual([e["step"] for e in evs if e["type"] == "checkpoint"], [2, 4])
            ck = os.path.join(d, "checkpoints", "0000002")
            self.assertTrue(P.verify_checkpoint(ck))
            self.assertEqual(sorted(os.listdir(ck)), ["adapter.safetensors", "manifest.json", "optimizer.safetensors", "state.json"])
            tensors, _ = P.read_safetensors(os.path.join(d, "adapter", "adapter_model.safetensors"))
            self.assertIn("base_model.model.model.layers.2.self_attn.q_proj.lora_A.weight", tensors)
            self.assertEqual(P.f32_values(tensors["base_model.model.model.layers.3.self_attn.v_proj.lora_B.weight"][2])[0], 1.5)
            with open(os.path.join(d, "adapter", "adapter_config.json")) as f:
                self.assertEqual(json.load(f)["target_modules"], ["q_proj", "v_proj"])
        finally:
            shutil.rmtree(d)

    def test_resume_restores_weights_and_optimizer(self):
        d = make_run(unsloth_job())
        try:
            self.assertEqual(self.capture(d, unsloth.UnslothBackend(FakeApi())), 0)
            P.write_json(os.path.join(d, "driver.json"), unsloth_job(attempt=1, resumeFrom=4, resumeDir="checkpoints/0000004"))
            api = FakeApi()
            self.assertEqual(self.capture(d, unsloth.UnslothBackend(api)), 0)
            names = [c[0] for c in api.calls]
            self.assertIn("load_lora", names)
            restored = [c for c in api.calls if c[0] == "load_optimizer"][0]
            self.assertEqual(restored[1], ["state.0.exp_avg"])
            self.assertEqual(restored[2], [{"lr": 0.0002}])
            # resumed at 4: two more steps, and B kept the four steps it had.
            self.assertEqual(len([c for c in api.calls if c[0] == "step"]), 2)
            tensors, _ = P.read_safetensors(os.path.join(d, "adapter", "adapter_model.safetensors"))
            self.assertEqual(P.f32_values(tensors["base_model.model.model.layers.2.self_attn.q_proj.lora_B.weight"][2])[0], 1.5)
        finally:
            shutil.rmtree(d)

    def test_no_gpu_off_linux_is_no_accelerator(self):
        if sys.platform.startswith("linux"):
            self.skipTest("on Linux the real API decides from torch.cuda")
        d = make_run(unsloth_job())
        try:
            out = open(os.path.join(d, "events.jsonl"), "a")
            old = sys.stdout
            sys.stdout = out
            try:
                code = sky_train.run(d)
            finally:
                sys.stdout = old
                out.close()
            self.assertEqual(code, 1)
            last = events(d)[-1]
            self.assertEqual((last["type"], last["code"]), ("error", "NoAccelerator"))
        finally:
            shutil.rmtree(d)


if __name__ == "__main__":
    unittest.main()
