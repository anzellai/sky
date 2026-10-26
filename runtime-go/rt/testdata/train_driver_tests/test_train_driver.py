"""Unit tests of the Sky training driver (runtime-go/rt/ai_train_assets/driver).

Standard library only, so they run under any CPython 3.10+:

    python3 -I -B -m unittest discover -s runtime-go/rt/testdata/train_driver_tests

`ai_python_suites_live_test.go` (go test -tags ailive) runs them under the
pinned CPython. The driver runs the Unsloth backend, and the Fake backend in
the Vertex container on a CPU machine.
"""

import json
import os
import random
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
DRIVER = os.path.normpath(os.path.join(HERE, "..", "..", "ai_train_assets", "driver"))
sys.path.insert(0, DRIVER)

import sky_protocol as P  # noqa: E402
import sky_rows as R  # noqa: E402
import sky_train  # noqa: E402
from backends import fake  # noqa: E402

SHAPES = {"layers": 4, "hidden": 8, "intermediate": 16, "qDim": 8, "kvDim": 4}


def job(**over):
    cfg = {
        "v": 1,
        "backend": "fake",
        "attempt": 0,
        "resumeFrom": 0,
        "seed": 7,
        "rank": 2,
        "alpha": 4,
        "layers": 2,
        "learningRate": 0.0001,
        "epochs": 1,
        "maxSteps": 6,
        "batch": {"perDevice": 2, "accumulate": 1},
        "sequenceLength": 64,
        "checkpointEvery": 2,
        "evalEvery": 3,
        "format": "chat",
        "base": {"id": "Qwen/Qwen2.5-0.5B-Instruct@test", "weights": ""},
        "shapes": SHAPES,
        "targets": ["self_attn.q_proj", "self_attn.v_proj"],
        "fakeStepMs": 0,
        "fakeCrashAt": 0,
    }
    cfg.update(over)
    return cfg


def make_run(cfg, rows=10):
    d = tempfile.mkdtemp(prefix="sky-train-test-")
    os.makedirs(os.path.join(d, "data"))
    row = {"messages": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]}
    for name, n in (("train.jsonl", rows), ("valid.jsonl", 2)):
        with open(os.path.join(d, "data", name), "w") as f:
            for _ in range(n):
                f.write(json.dumps(row) + "\n")
    P.write_json(os.path.join(d, "driver.json"), cfg)
    return d


def run_driver(run_dir):
    """The driver as the Go runtime starts it: a separate process, isolated."""
    with open(os.path.join(run_dir, "events.jsonl"), "ab") as out:
        p = subprocess.run(
            [sys.executable, "-I", "-B", os.path.join(DRIVER, "sky_train.py"), "--run", run_dir, "--launch", "t"],
            stdout=out,
            stderr=subprocess.PIPE,
            cwd=run_dir,
            timeout=120,
        )
    return p.returncode


def events(run_dir):
    with open(os.path.join(run_dir, "events.jsonl")) as f:
        return [json.loads(line) for line in f if line.strip()]


class ProtocolTest(unittest.TestCase):
    def test_lines_are_versioned_sorted_json(self):
        import io

        buf = io.StringIO()
        old = sys.stdout
        sys.stdout = buf
        try:
            P.emit("progress", step=3, loss=1.5, total=9)
            P.emit("metric", step=3, name="val_loss", value=float("nan"))
        finally:
            sys.stdout = old
        lines = buf.getvalue().splitlines()
        self.assertEqual(lines[0], '{"loss":1.5,"step":3,"total":9,"type":"progress","v":1}')
        self.assertIsNone(json.loads(lines[1])["value"])

    def test_claimed_stdout_holds_protocol_lines_only(self):
        code = "import sys; sys.path.insert(0, %r); import sky_protocol as P; P.claim_stdout(); print('library noise'); P.emit('hello', backend='fake')" % DRIVER
        p = subprocess.run([sys.executable, "-I", "-B", "-c", code], capture_output=True, text=True, timeout=60)
        self.assertEqual(p.stdout, '{"backend":"fake","type":"hello","v":1}\n')
        self.assertIn("library noise", p.stderr)

    def test_safetensors_round_trip(self):
        d = tempfile.mkdtemp()
        try:
            path = os.path.join(d, "t.safetensors")
            P.write_safetensors(path, {"b": ("F32", (2, 1), P.f32_bytes([1.0, 2.0])), "a": ("F32", (1,), P.f32_bytes([3.5]))}, {"k": "v"})
            tensors, meta = P.read_safetensors(path)
            self.assertEqual(meta, {"k": "v"})
            self.assertEqual(P.f32_values(tensors["b"][2]), [1.0, 2.0])
            self.assertEqual(tensors["b"][1], (2, 1))
            with open(path, "rb") as f:
                n = int.from_bytes(f.read(8), "little")
            self.assertEqual(n % 8, 0)
        finally:
            shutil.rmtree(d)


class DataOrderTest(unittest.TestCase):
    def test_order_depends_on_seed_and_step_only(self):
        a = [P.batch_indices(10, 3, 7, s) for s in range(8)]
        b = [P.batch_indices(10, 3, 7, s) for s in range(8)]
        self.assertEqual(a, b)
        self.assertNotEqual(a, [P.batch_indices(10, 3, 8, s) for s in range(8)])

    def test_each_epoch_is_a_permutation(self):
        seen = []
        for s in range(5):
            seen += P.batch_indices(10, 2, 1, s)
        self.assertEqual(sorted(seen), list(range(10)))

    def test_resume_reads_the_same_batches(self):
        whole = [P.batch_indices(7, 4, 3, s) for s in range(6)]
        resumed = [P.batch_indices(7, 4, 3, s) for s in range(3, 6)]
        self.assertEqual(whole[3:], resumed)

    def test_total_steps(self):
        self.assertEqual(P.total_steps({"maxSteps": 5}, 100), 5)
        cfg = {"maxSteps": 0, "epochs": 2, "batch": {"perDevice": 4, "accumulate": 2}}
        self.assertEqual(P.total_steps(cfg, 20), 5)


class MlxToPeftTest(unittest.TestCase):
    def test_names(self):
        self.assertEqual(
            P.mlx_to_peft_name("model.layers.3.self_attn.q_proj.lora_a"),
            "base_model.model.model.layers.3.self_attn.q_proj.lora_A.weight",
        )
        self.assertEqual(
            P.mlx_to_peft_name("model.layers.3.mlp.down_proj.lora_b"),
            "base_model.model.model.layers.3.mlp.down_proj.lora_B.weight",
        )
        self.assertIsNone(P.mlx_to_peft_name("model.layers.3.mlp.down_proj.weight"))

    def test_delta_is_identical(self):
        # MLX: y += scale * (x @ lora_a) @ lora_b, lora_a (in, r), lora_b (r, out).
        # PEFT: y += (alpha / r) * B (A x), A (r, in), B (out, r).
        rng = random.Random(1)
        din, dout, r, scale = 5, 3, 2, 2.5
        lora_a = [[rng.uniform(-1, 1) for _ in range(r)] for _ in range(din)]
        lora_b = [[rng.uniform(-1, 1) for _ in range(dout)] for _ in range(r)]
        x = [rng.uniform(-1, 1) for _ in range(din)]
        xa = [sum(x[i] * lora_a[i][k] for i in range(din)) for k in range(r)]
        mlx = [scale * sum(xa[k] * lora_b[k][j] for k in range(r)) for j in range(dout)]
        A, B = P.transpose(lora_a), P.transpose(lora_b)
        alpha = scale * r
        ax = [sum(A[k][i] * x[i] for i in range(din)) for k in range(r)]
        peft = [(alpha / r) * sum(B[j][k] * ax[k] for k in range(r)) for j in range(dout)]
        for m, p in zip(mlx, peft):
            self.assertAlmostEqual(m, p, places=12)
        self.assertEqual((len(A), len(A[0])), (r, din))
        self.assertEqual((len(B), len(B[0])), (dout, r))


class RowsTest(unittest.TestCase):
    class Tok(object):
        pad_token_id = 0

        def apply_chat_template(self, msgs, tokenize=True, add_generation_prompt=False):
            ids = []
            for m in msgs:
                ids += [1] + [ord(c) for c in m["content"]] + [2]
            if add_generation_prompt:
                ids += [9]
            return ids

    def test_instruction_rows_become_a_conversation(self):
        ids, prompt = R.encode(self.Tok(), {"prompt": "ab", "completion": "c"}, "instruction", 64)
        self.assertEqual(ids, [1, 97, 98, 2, 1, 99, 2])
        self.assertEqual(prompt, 5)

    def test_mask_covers_the_answer_only(self):
        mask = R.loss_mask([5], [3], 6)
        self.assertEqual(mask, [[0.0, 0.0, 1.0, 1.0, 0.0]])


class FakeRunTest(unittest.TestCase):
    def test_full_run_writes_protocol_checkpoints_and_adapter(self):
        d = make_run(job())
        try:
            self.assertEqual(run_driver(d), 0)
            evs = events(d)
            types = [e["type"] for e in evs]
            self.assertEqual(types[0], "hello")
            self.assertEqual(evs[0]["totalSteps"], 6)
            self.assertEqual(types[-2:], ["artefact", "done"])
            self.assertEqual([e["step"] for e in evs if e["type"] == "checkpoint"], [2, 4])
            self.assertEqual([e["step"] for e in evs if e["type"] == "progress"], [1, 2, 3, 4, 5, 6])
            for e in evs:
                self.assertEqual(e["v"], 1)
            for name in os.listdir(os.path.join(d, "checkpoints")):
                ck = os.path.join(d, "checkpoints", name)
                self.assertTrue(P.verify_checkpoint(ck))
                for f in os.listdir(ck):
                    self.assertTrue(f.endswith(".safetensors") or f.endswith(".json"), f)
            tensors, _ = P.read_safetensors(os.path.join(d, "adapter", "adapter_model.safetensors"))
            # 2 of the 4 layers, 2 targets, A and B each.
            self.assertEqual(len(tensors), 8)
            a = tensors["base_model.model.model.layers.3.self_attn.v_proj.lora_A.weight"]
            b = tensors["base_model.model.model.layers.3.self_attn.v_proj.lora_B.weight"]
            self.assertEqual(a[1], (2, 8))
            self.assertEqual(b[1], (4, 2))
            self.assertEqual(set(P.f32_values(b[2])), {0.0})
            with open(os.path.join(d, "adapter", "adapter_config.json")) as f:
                conf = json.load(f)
            self.assertEqual((conf["r"], conf["lora_alpha"], conf["peft_type"]), (2, 4, "LORA"))
            art = [e for e in evs if e["type"] == "artefact"][0]
            self.assertEqual(art["sha256"], P.sha256_file(os.path.join(d, "adapter", "adapter_model.safetensors")))
        finally:
            shutil.rmtree(d)

    def test_crash_then_resume_continues_the_same_curve(self):
        whole = make_run(job())
        crashed = make_run(job(fakeCrashAt=3))
        try:
            self.assertEqual(run_driver(whole), 0)
            self.assertEqual(run_driver(crashed), 137)
            evs = events(crashed)
            self.assertNotIn("done", [e["type"] for e in evs])
            self.assertEqual([e["step"] for e in evs if e["type"] == "checkpoint"], [2])
            cfg = job(fakeCrashAt=3, attempt=1, resumeFrom=2, resumeDir="checkpoints/0000002")
            P.write_json(os.path.join(crashed, "driver.json"), cfg)
            self.assertEqual(run_driver(crashed), 0)
            evs = events(crashed)
            hello = [e for e in evs if e["type"] == "hello"]
            self.assertEqual(hello[1]["resumeFrom"], 2)
            loss_whole = {e["step"]: e["loss"] for e in events(whole) if e["type"] == "progress"}
            loss_resumed = {e["step"]: e["loss"] for e in evs if e["type"] == "progress"}
            self.assertEqual(loss_whole, loss_resumed)
            self.assertEqual(evs[-1]["type"], "done")
        finally:
            shutil.rmtree(whole)
            shutil.rmtree(crashed)

    def test_corrupt_checkpoint_is_refused(self):
        d = make_run(job())
        try:
            self.assertEqual(run_driver(d), 0)
            ck = os.path.join(d, "checkpoints", "0000004")
            with open(os.path.join(ck, "optimizer.safetensors"), "ab") as f:
                f.write(b"x")
            self.assertFalse(P.verify_checkpoint(ck))
            P.write_json(os.path.join(d, "driver.json"), job(attempt=1, resumeFrom=4, resumeDir="checkpoints/0000004"))
            self.assertEqual(run_driver(d), 1)
            last = events(d)[-1]
            self.assertEqual((last["type"], last["code"]), ("error", "TrainingFailed"))
        finally:
            shutil.rmtree(d)

    def test_cancel_marker_checkpoints_and_stops(self):
        d = make_run(job(maxSteps=50, checkpointEvery=0))
        try:
            open(os.path.join(d, "cancel"), "w").close()
            self.assertEqual(run_driver(d), 0)
            evs = events(d)
            self.assertEqual(evs[-1]["type"], "cancelled")
            self.assertEqual(evs[-1]["step"], 0)
        finally:
            shutil.rmtree(d)

    def test_cancel_mid_run_writes_a_checkpoint(self):
        d = make_run(job(maxSteps=400, checkpointEvery=0, fakeStepMs=10))
        try:
            with open(os.path.join(d, "events.jsonl"), "ab") as out:
                p = subprocess.Popen(
                    [sys.executable, "-I", "-B", os.path.join(DRIVER, "sky_train.py"), "--run", d, "--launch", "t"],
                    stdout=out,
                    cwd=d,
                )
                import time

                deadline = time.time() + 30
                while time.time() < deadline:
                    with open(os.path.join(d, "events.jsonl")) as f:
                        if sum(1 for _ in f) > 5:
                            break
                    time.sleep(0.05)
                p.terminate()
                self.assertEqual(p.wait(timeout=60), 0)
            evs = events(d)
            last = evs[-1]
            self.assertEqual(last["type"], "cancelled")
            self.assertGreater(last["step"], 0)
            ck = [e for e in evs if e["type"] == "checkpoint"]
            self.assertEqual(ck[-1]["step"], last["step"])
        finally:
            shutil.rmtree(d)

    def test_empty_eval_split_is_dataset_invalid(self):
        d = make_run(job())
        try:
            open(os.path.join(d, "data", "valid.jsonl"), "w").close()
            self.assertEqual(run_driver(d), 1)
            last = events(d)[-1]
            self.assertEqual((last["type"], last["code"]), ("error", "DatasetInvalid"))
        finally:
            shutil.rmtree(d)

    def test_fake_adapter_shapes(self):
        cfg = job(layers=-1, targets=["self_attn.q_proj", "self_attn.k_proj", "self_attn.o_proj", "mlp.gate_proj", "mlp.down_proj"])
        self.assertEqual(fake.lora_layers(cfg), [0, 1, 2, 3])
        self.assertEqual(fake.target_dims("self_attn.k_proj", SHAPES), (8, 4))
        self.assertEqual(fake.target_dims("mlp.down_proj", SHAPES), (16, 8))


if __name__ == "__main__":
    unittest.main()
