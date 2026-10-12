"""Unit tests of the cloud shim (runtime-go/rt/ai_train_assets/driver/sky_cloud.py).

Standard library only, offline. A fake server on 127.0.0.1 answers the
metadata token endpoint, the Cloud Storage JSON API (media upload with
ifGenerationMatch, media read, list with prefix and pageToken) and a Hugging
Face resolve endpoint. The real shim runs as a subprocess with the Fake
backend:

    python3 -I -B -m unittest discover -s runtime-go/rt/testdata/train_driver_tests -p test_sky_cloud.py -v
"""

import hashlib
import http.server
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
DRIVER = os.path.normpath(os.path.join(HERE, "..", "..", "ai_train_assets", "driver"))
sys.path.insert(0, DRIVER)

import sky_cloud  # noqa: E402

TOKEN = "ya29.fake-SECRET-token-7c1e"
BUCKET = "sky-stage"
PREFIX = "team/ai"
KEY = "run-abc123"
RUN = PREFIX + "/runs/" + KEY
SHAPES = {"layers": 4, "hidden": 8, "intermediate": 16, "qDim": 8, "kvDim": 4}


def sha(b):
    return hashlib.sha256(b).hexdigest()


def driver_cfg(**over):
    cfg = {
        "v": 1,
        "backend": "fake",
        "attempt": 0,
        "resumeFrom": 0,
        "resumeDir": "",
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
        "quantised": False,
        "python": "",
        "driverDir": "",
        "toolchain": "vertex",
    }
    cfg.update(over)
    return cfg


class FakeCloud(object):
    """The objects, the upload order and the requests the fake has seen."""

    def __init__(self):
        self.lock = threading.Lock()
        self.objects = {}
        self.order = []
        self.hf = {}
        self.hf_auth = []
        self.unauthorised = 0
        self.page = 2

    def put(self, name, data):
        with self.lock:
            self.objects[name] = data
            self.order.append(name)

    def get(self, name):
        with self.lock:
            return self.objects.get(name)

    def names(self, prefix=""):
        with self.lock:
            return sorted(n for n in self.objects if n.startswith(prefix))


def make_handler(cloud):
    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def send(self, status, body=b"", ctype="application/json"):
            if isinstance(body, (dict, list)):
                body = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def authorised(self):
            if self.headers.get("Authorization") != "Bearer " + TOKEN:
                cloud.unauthorised += 1
                self.send(401, {"error": "unauthorised"})
                return False
            return True

        def do_GET(self):
            u = urllib.parse.urlsplit(self.path)
            q = urllib.parse.parse_qs(u.query)
            path = u.path
            if path == "/metadata/computeMetadata/v1/instance/service-accounts/default/token":
                if self.headers.get("Metadata-Flavor") != "Google":
                    return self.send(403, {"error": "flavor"})
                return self.send(200, {"access_token": TOKEN, "expires_in": 3600, "token_type": "Bearer"})
            if path.startswith("/hf/"):
                cloud.hf_auth.append(self.headers.get("Authorization"))
                data = cloud.hf.get(urllib.parse.unquote(path[len("/hf/") :]))
                if data is None:
                    return self.send(404, b"no")
                return self.send(200, data, "application/octet-stream")
            pre = "/storage/storage/v1/b/%s/o" % BUCKET
            if not path.startswith(pre):
                return self.send(404, {"error": path})
            if not self.authorised():
                return
            rest = path[len(pre) :]
            if rest == "":
                prefix = q.get("prefix", [""])[0]
                names = cloud.names(prefix)
                start = int(q.get("pageToken", ["0"])[0])
                body = {"items": [{"name": n} for n in names[start : start + cloud.page]]}
                if start + cloud.page < len(names):
                    body["nextPageToken"] = str(start + cloud.page)
                return self.send(200, body)
            name = urllib.parse.unquote(rest[1:])
            if q.get("alt") != ["media"]:
                return self.send(400, {"error": "alt"})
            data = cloud.get(name)
            if data is None:
                return self.send(404, {"error": "not found"})
            return self.send(200, data, "application/octet-stream")

        def do_POST(self):
            u = urllib.parse.urlsplit(self.path)
            q = urllib.parse.parse_qs(u.query)
            n = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(n)
            if u.path != "/storage/upload/storage/v1/b/%s/o" % BUCKET:
                return self.send(404, {"error": u.path})
            if not self.authorised():
                return
            if q.get("uploadType") != ["media"]:
                return self.send(400, {"error": "uploadType"})
            name = q["name"][0]
            if q.get("ifGenerationMatch") == ["0"] and cloud.get(name) is not None:
                return self.send(412, {"error": "conditionNotMet"})
            cloud.put(name, body)
            return self.send(200, {"name": name})

    return Handler


TRAIN_ROWS = b"".join(
    json.dumps({"messages": [{"role": "user", "content": "hi %d" % i}, {"role": "assistant", "content": "hello"}]}).encode() + b"\n"
    for i in range(10)
)
VALID_ROWS = TRAIN_ROWS[: TRAIN_ROWS.index(b"\n") + 1] * 2


class ShimTest(unittest.TestCase):
    def setUp(self):
        self.cloud = FakeCloud()
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), make_handler(self.cloud))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]
        self.tmp = tempfile.mkdtemp(prefix="sky-cloud-test-")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        shutil.rmtree(self.tmp, ignore_errors=True)

    # Helpers ---------------------------------------------------------------

    def stage(self, driver, base=None, train=TRAIN_ROWS, train_digest=None):
        for data in (train, VALID_ROWS):
            self.cloud.put(PREFIX + "/blobs/sha256/" + sha(data), data)
        inputs = {
            "v": 1,
            "runKey": KEY,
            "store": "gs://%s/%s" % (BUCKET, PREFIX),
            "run": "runs/" + KEY,
            "driver": driver,
            "data": {
                "train": {"sha256": train_digest or sha(train), "size": len(train)},
                "valid": {"sha256": sha(VALID_ROWS), "size": len(VALID_ROWS)},
            },
            "base": base,
        }
        self.cloud.put(RUN + "/inputs.json", json.dumps(inputs).encode())
        self.cloud.order = []

    def work(self, attempt):
        return os.path.join(self.tmp, "work-%d" % attempt)

    def command(self, attempt=0, resume=0):
        return [
            sys.executable,
            "-I",
            "-B",
            os.path.join(DRIVER, "sky_cloud.py"),
            "--inputs",
            "gs://%s/%s/inputs.json" % (BUCKET, RUN),
            "--run",
            "gs://%s/%s" % (BUCKET, RUN),
            "--attempt",
            str(attempt),
            "--resume-from",
            str(resume),
        ]

    def env(self, attempt):
        return {
            "PATH": "/usr/bin:/bin",
            "SKY_AI_GCP_ENDPOINT": self.url,
            "SKY_AI_HF_ENDPOINT": self.url + "/hf",
            "SKY_CLOUD_WORK": self.work(attempt),
            "SKY_CLOUD_FLUSH_S": "0.05",
            "CLOUD_ML_JOB_ID": "4242",
        }

    def run_shim(self, attempt=0, resume=0):
        p = subprocess.run(self.command(attempt, resume), env=self.env(attempt), capture_output=True, timeout=60)
        self.assertNotIn(TOKEN.encode(), p.stderr)
        return p

    def segments(self, attempt):
        prefix = RUN + "/attempt-%d/events/" % attempt
        names = self.cloud.names(prefix)
        self.assertEqual(names, [prefix + "%08d.jsonl" % i for i in range(len(names))])
        return names

    def events(self, attempt):
        out = []
        for n in self.segments(attempt):
            body = self.cloud.get(n).decode()
            self.assertTrue(body.endswith("\n"))
            out += [json.loads(line) for line in body.splitlines()]
        return out

    def assert_no_token_anywhere(self):
        for n in self.cloud.names():
            self.assertNotIn(TOKEN.encode(), self.cloud.get(n), n)
        self.assertEqual(self.cloud.unauthorised, 0)
        for auth in self.cloud.hf_auth:
            self.assertIsNone(auth)

    # Cases -----------------------------------------------------------------

    def test_full_run(self):
        self.stage(driver_cfg())
        p = self.run_shim()
        self.assertEqual(p.returncode, 0, p.stderr.decode())
        hello = json.loads(self.cloud.get(RUN + "/attempt-0/hello.json"))
        self.assertEqual((hello["jobId"], hello["attempt"]), ("4242", 0))
        self.assertIsInstance(hello["at"], int)
        self.assertGreaterEqual(len(self.segments(0)), 1)
        evs = self.events(0)
        types = [e["type"] for e in evs]
        self.assertEqual(types.count("hello"), 1)
        self.assertEqual(types.count("done"), 1)
        self.assertEqual(types[0], "hello")
        self.assertEqual(types[-1], "done")
        order = self.cloud.order
        # Each segment's checkpoint lines name checkpoints whose files and
        # manifest were written before the segment.
        for seg in self.segments(0):
            at = order.index(seg)
            for line in self.cloud.get(seg).decode().splitlines():
                e = json.loads(line)
                if e["type"] == "checkpoint":
                    d = RUN + "/checkpoints/%07d/" % e["step"]
                    manifest = json.loads(self.cloud.get(d + "manifest.json"))
                    m_at = order.index(d + "manifest.json")
                    self.assertLess(m_at, at)
                    for f, digest in manifest["files"].items():
                        self.assertEqual(sha(self.cloud.get(d + f)), digest)
                        self.assertLess(order.index(d + f), m_at)
                if e["type"] == "artefact":
                    self.assertLess(order.index(RUN + "/adapter/manifest.json"), at)
        self.assertEqual([e["step"] for e in evs if e["type"] == "checkpoint"], [2, 4])
        manifest = json.loads(self.cloud.get(RUN + "/adapter/manifest.json"))
        names = [f["name"] for f in manifest["files"]]
        self.assertEqual(names, ["adapter_config.json", "adapter_model.safetensors"])
        for f in manifest["files"]:
            self.assertEqual(sha(self.cloud.get(RUN + "/adapter/" + f["name"])), f["sha256"])
            self.assertLess(order.index(RUN + "/adapter/" + f["name"]), order.index(RUN + "/adapter/manifest.json"))
        art = [e for e in evs if e["type"] == "artefact"][0]
        self.assertEqual(art["sha256"], sha(self.cloud.get(RUN + "/adapter/adapter_model.safetensors")))
        self.assertEqual(json.loads(self.cloud.get(RUN + "/done.json")), {"attempt": 0, "step": 6})
        with open(os.path.join(self.work(0), "run", "driver.json")) as f:
            cfg = json.load(f)
        self.assertEqual((cfg["attempt"], cfg["resumeFrom"], cfg["resumeDir"]), (0, 0, ""))
        self.assertEqual(cfg["driverDir"], DRIVER)
        self.assertEqual(cfg["base"]["weights"], "")
        self.assertNotIn(TOKEN.encode(), p.stderr)
        self.assert_no_token_anywhere()

    def test_resume_from_a_checkpoint(self):
        self.stage(driver_cfg())
        self.assertEqual(self.run_shim(0).returncode, 0)
        p = self.run_shim(attempt=1, resume=4)
        self.assertEqual(p.returncode, 0, p.stderr.decode())
        evs = self.events(1)
        hello = [e for e in evs if e["type"] == "hello"]
        self.assertEqual(len(hello), 1)
        self.assertEqual((hello[0]["resumeFrom"], hello[0]["attempt"]), (4, 1))
        self.assertEqual(evs[-1]["type"], "done")
        self.assertEqual([e["step"] for e in evs if e["type"] == "progress"], [5, 6])
        self.assertEqual(json.loads(self.cloud.get(RUN + "/done.json")), {"attempt": 1, "step": 6})
        local = os.path.join(self.work(1), "run", "checkpoints", "0000004", "manifest.json")
        with open(local, "rb") as f:
            self.assertEqual(f.read(), self.cloud.get(RUN + "/checkpoints/0000004/manifest.json"))
        self.assert_no_token_anywhere()

    def test_cancel_from_the_bucket(self):
        self.stage(driver_cfg(maxSteps=2000, checkpointEvery=0, fakeStepMs=50))
        proc = subprocess.Popen(self.command(), env=self.env(0), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            deadline = time.time() + 20
            while time.time() < deadline and not self.cloud.names(RUN + "/attempt-0/events/"):
                time.sleep(0.05)
            self.cloud.put(RUN + "/cancel", b"")
            _, err = proc.communicate(timeout=30)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
        self.assertEqual(proc.returncode, 0, err.decode())
        self.assertNotIn(TOKEN.encode(), err)
        evs = self.events(0)
        self.assertEqual(evs[-1]["type"], "cancelled")
        self.assertGreater(evs[-1]["step"], 0)
        self.assertLess(evs[-1]["step"], 2000)
        ck = [e for e in evs if e["type"] == "checkpoint"]
        self.assertEqual(ck[-1]["step"], evs[-1]["step"])
        self.assertIsNotNone(self.cloud.get(RUN + "/checkpoints/%07d/manifest.json" % ck[-1]["step"]))
        self.assertIsNone(self.cloud.get(RUN + "/done.json"))
        self.assert_no_token_anywhere()

    def test_sigterm_to_the_shim_stops_the_driver(self):
        self.stage(driver_cfg(maxSteps=2000, checkpointEvery=0, fakeStepMs=50))
        proc = subprocess.Popen(self.command(), env=self.env(0), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            deadline = time.time() + 20
            while time.time() < deadline and not self.cloud.names(RUN + "/attempt-0/events/"):
                time.sleep(0.05)
            proc.terminate()
            _, err = proc.communicate(timeout=30)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
        self.assertEqual(proc.returncode, 0, err.decode())
        self.assertEqual(self.events(0)[-1]["type"], "cancelled")
        self.assertIsNone(self.cloud.get(RUN + "/done.json"))

    def test_crash_uploads_the_stderr_tail(self):
        self.stage(driver_cfg(fakeCrashAt=3))
        p = self.run_shim()
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(p.returncode, 137)
        types = [e["type"] for e in self.events(0)]
        for t in ("done", "error", "cancelled"):
            self.assertNotIn(t, types)
        self.assertIsNotNone(self.cloud.get(RUN + "/attempt-0/stderr.txt"))
        self.assertIsNotNone(self.cloud.get(RUN + "/checkpoints/0000002/manifest.json"))
        self.assertIsNone(self.cloud.get(RUN + "/done.json"))
        self.assert_no_token_anywhere()

    def test_train_blob_digest_mismatch(self):
        self.stage(driver_cfg(), train_digest=sha(b"something else"))
        # The blob under the claimed digest holds other bytes.
        self.cloud.put(PREFIX + "/blobs/sha256/" + sha(b"something else"), TRAIN_ROWS)
        p = self.run_shim()
        self.assertEqual(p.returncode, 1)
        evs = self.events(0)
        self.assertEqual(len(evs), 1)
        self.assertEqual((evs[0]["type"], evs[0]["code"], evs[0]["v"]), ("error", "ChecksumMismatch", 1))
        self.assertIsNone(self.cloud.get(RUN + "/done.json"))
        self.assert_no_token_anywhere()

    def test_missing_train_blob_is_dataset_invalid(self):
        self.stage(driver_cfg(), train_digest="0" * 64)
        p = self.run_shim()
        self.assertEqual(p.returncode, 1)
        evs = self.events(0)
        self.assertEqual((evs[0]["type"], evs[0]["code"]), ("error", "DatasetInvalid"))

    def test_base_snapshot_is_downloaded_and_checked(self):
        files = {"config.json": b'{"model_type":"qwen2"}', "sub/model.safetensors": b"\x00weights\x01" * 50}
        for path, data in files.items():
            self.cloud.hf["Org/Model/resolve/abc123/" + path] = data
        base = {"repo": "Org/Model", "revision": "abc123", "files": [{"path": p, "sha256": sha(d)} for p, d in files.items()]}
        self.stage(driver_cfg(), base=base)
        p = self.run_shim()
        self.assertEqual(p.returncode, 0, p.stderr.decode())
        root = os.path.join(self.work(0), "base")
        for path, data in files.items():
            with open(os.path.join(root, path), "rb") as f:
                self.assertEqual(f.read(), data)
        with open(os.path.join(self.work(0), "run", "driver.json")) as f:
            cfg = json.load(f)
        self.assertEqual(cfg["base"], {"id": "Qwen/Qwen2.5-0.5B-Instruct@test", "weights": root})
        self.assertTrue(self.cloud.hf_auth)
        self.assert_no_token_anywhere()

    def test_base_digest_mismatch(self):
        self.cloud.hf["Org/Model/resolve/abc123/config.json"] = b"tampered"
        base = {"repo": "Org/Model", "revision": "abc123", "files": [{"path": "config.json", "sha256": sha(b"original")}]}
        self.stage(driver_cfg(), base=base)
        env = self.env(0)
        p = subprocess.run(self.command(), env=env, capture_output=True, timeout=60)
        self.assertEqual(p.returncode, 1)
        self.assertEqual(self.events(0)[0]["code"], "ChecksumMismatch")


class UnitTest(unittest.TestCase):
    def test_split_gs(self):
        self.assertEqual(sky_cloud.split_gs("gs://b/a/b/c/"), ("b", "a/b/c"))
        self.assertEqual(sky_cloud.split_gs("gs://b"), ("b", ""))
        with self.assertRaises(sky_cloud.ShimError):
            sky_cloud.split_gs("s3://b/x")

    def test_unsafe_base_paths_are_refused(self):
        for bad in ("../x", "/etc/passwd", "a/../../b", "a//b", ""):
            with self.assertRaises(sky_cloud.ShimError):
                sky_cloud.safe_relative(bad)
        self.assertEqual(sky_cloud.safe_relative("a/b.json"), os.path.join("a", "b.json"))

    def test_driver_env_uses_cuda_compat_on_an_old_driver(self):
        class P(object):
            stdout = "575.57.08\n"

        env = sky_cloud.driver_env({"LD_LIBRARY_PATH": "/x"}, run=lambda *a, **k: P(), which=lambda n: "/usr/bin/nvidia-smi", exists=lambda p: True)
        self.assertEqual(env["LD_LIBRARY_PATH"], "/usr/local/cuda/compat:/x")
        P.stdout = "580.65.06\n"
        env = sky_cloud.driver_env({}, run=lambda *a, **k: P(), which=lambda n: "/usr/bin/nvidia-smi", exists=lambda p: True)
        self.assertNotIn("LD_LIBRARY_PATH", env)
        env = sky_cloud.driver_env({}, which=lambda n: None)
        self.assertNotIn("LD_LIBRARY_PATH", env)

    def test_partial_lines_wait_for_their_newline(self):
        d = tempfile.mkdtemp()
        try:
            with open(os.path.join(d, "events.jsonl"), "wb") as f:
                f.write(b'{"type":"progress","v":1}\n{"type":"pro')
            m = sky_cloud.Mirror(None, "b", "r", 0, d)
            lines, end = m.new_lines()
            self.assertEqual(lines, ['{"type":"progress","v":1}'])
            self.assertEqual(end, 26)
        finally:
            shutil.rmtree(d)


if __name__ == "__main__":
    unittest.main()
