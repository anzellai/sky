"""The Sky trainer's cloud shim: Google Cloud Storage on one side, the
unchanged training driver (sky_train.py) on the other.

A Vertex AI custom job runs this program as its container's entry point:

    sky_cloud.py --inputs gs://<bucket>/<prefix>/runs/<key>/inputs.json
                 --run gs://<bucket>/<prefix>/runs/<key>
                 --attempt <a> --resume-from <step>

It stages the run on the local disk (the data blobs, the pinned base
snapshot, the checkpoint it resumes from), writes the driver's driver.json,
starts the driver, and mirrors what the driver writes back to the bucket:

    runs/<key>/attempt-<a>/hello.json           first, so the Sky side can
                                                attach to the job
    runs/<key>/attempt-<a>/events/<seq>.jsonl   protocol segments, in order
    runs/<key>/checkpoints/<step>/...           manifest.json last
    runs/<key>/adapter/...                      manifest.json last
    runs/<key>/done.json                        the run finished
    runs/<key>/attempt-<a>/stderr.txt           a crash's last words

A segment is uploaded only after every file its lines name, so a reader that
sees a `checkpoint` or `artefact` line finds the files complete. The object
layout matches the Go emulator (runtime-go/rt/ai_gcpfake.go, `advance`) and
the Sky side (sky-stdlib/Std/Ai/Train.sky, "The Vertex backend").

Environment:

    SKY_AI_GCP_ENDPOINT   an emulator base URL (<url>/storage, <url>/metadata)
    CLOUD_ML_JOB_ID       the Vertex job's numeric id
    SKY_AI_HF_ENDPOINT    replaces https://huggingface.co for base weights
    SKY_CLOUD_WORK        the local work directory (default /tmp/sky-run)
    SKY_CLOUD_FLUSH_S     seconds between uploads of new lines (default 5)

The access token comes from the metadata server. It is sent only in an
Authorization header to Cloud Storage, and it never appears in a log line,
an uploaded object or an exception message.

Standard library only.
"""

import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))

RETRY_TRIES = 6
TERM_GRACE_S = 60.0
STDERR_TAIL = 4000
CUDA_COMPAT = "/usr/local/cuda/compat"


class ShimError(Exception):
    """A failure before the driver starts, with a Std.Ai.Failure code."""

    def __init__(self, code, message):
        Exception.__init__(self, message)
        self.code = code
        self.message = message


class HttpStatus(Exception):
    """A response with a status the caller has to decide about."""

    def __init__(self, status, what):
        Exception.__init__(self, "HTTP %d for %s" % (status, what))
        self.status = status


def log(text):
    """One line on standard error (the job's log)."""
    sys.stderr.write("sky_cloud: " + text + "\n")
    sys.stderr.flush()


def step_name(step):
    return "%07d" % int(step)


def segment_name(seq):
    return "%08d" % int(seq)


def split_gs(url):
    """gs://bucket/name to (bucket, name). The name has no trailing slash."""
    if not url.startswith("gs://"):
        raise ShimError("TrainingFailed", "not a gs:// location: %s" % url)
    rest = url[len("gs://") :]
    bucket, _, name = rest.partition("/")
    if not bucket:
        raise ShimError("TrainingFailed", "no bucket in %s" % url)
    return bucket, name.strip("/")


def join_key(prefix, name):
    return name if not prefix else prefix + "/" + name


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Refuse redirects, so the Authorization header never follows one."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def make_opener(direct, redirects):
    """An opener. `direct` skips any proxy (an emulator on loopback);
    `redirects` False refuses redirects."""
    handlers = []
    if direct:
        handlers.append(urllib.request.ProxyHandler({}))
    if not redirects:
        handlers.append(_NoRedirect())
    return urllib.request.build_opener(*handlers)


def retrying(what, attempt_fn, sleep, tries=RETRY_TRIES):
    """Run attempt_fn, retrying 429, 5xx and connection errors with an
    exponential backoff. Other statuses raise HttpStatus at once."""
    delay = 0.5
    for i in range(tries):
        try:
            return attempt_fn()
        except urllib.error.HTTPError as e:
            status = e.code
            e.close()
            if status != 429 and status < 500:
                raise HttpStatus(status, what)
            if i == tries - 1:
                raise HttpStatus(status, what)
        except (urllib.error.URLError, OSError) as e:
            if i == tries - 1:
                raise ShimError("TrainingFailed", "%s: %s" % (what, type(e).__name__))
        sleep(min(delay, 8.0))
        delay *= 2
    raise ShimError("TrainingFailed", what)


class Cloud(object):
    """Cloud Storage and the metadata server, with a cached access token."""

    def __init__(self, environ, clock=time.time, sleep=time.sleep):
        endpoint = environ.get("SKY_AI_GCP_ENDPOINT", "").rstrip("/")
        if endpoint:
            self.gcs = endpoint + "/storage"
            self.metadata = endpoint + "/metadata"
        else:
            self.gcs = "https://storage.googleapis.com"
            self.metadata = "http://" + environ.get("GCE_METADATA_HOST", "metadata.google.internal")
        self.opener = make_opener(bool(endpoint), redirects=False)
        self.clock = clock
        self.sleep = sleep
        self._token = None
        self._expires = 0.0

    # The token -------------------------------------------------------------

    def token(self):
        """The access token, fetched again 60 s before it expires."""
        if self._token is not None and self.clock() < self._expires - 60:
            return self._token
        url = self.metadata + "/computeMetadata/v1/instance/service-accounts/default/token"

        def once():
            req = urllib.request.Request(url, headers={"Metadata-Flavor": "Google"})
            with self.opener.open(req, timeout=30) as r:
                return json.loads(r.read().decode("utf-8"))

        try:
            body = retrying("the metadata token", once, self.sleep)
        except HttpStatus as e:
            raise ShimError("TrainingFailed", "the metadata server refused a token (HTTP %d)" % e.status)
        except ValueError:
            raise ShimError("TrainingFailed", "the metadata server's token is not JSON")
        tok = body.get("access_token") if isinstance(body, dict) else None
        if not tok:
            raise ShimError("TrainingFailed", "the metadata server returned no access token")
        self._token = tok
        self._expires = self.clock() + float(body.get("expires_in", 300))
        return tok

    def _request(self, method, url, data=None, headers=None):
        h = dict(headers or {})
        h["Authorization"] = "Bearer " + self.token()
        return urllib.request.Request(url, data=data, headers=h, method=method)

    # Objects --------------------------------------------------------------

    def _object_url(self, bucket, name):
        return "%s/storage/v1/b/%s/o/%s" % (self.gcs, urllib.parse.quote(bucket, safe=""), urllib.parse.quote(name, safe=""))

    def _upload_url(self, bucket, name, create_only):
        q = "uploadType=media&name=" + urllib.parse.quote(name, safe="")
        if create_only:
            q += "&ifGenerationMatch=0"
        return "%s/upload/storage/v1/b/%s/o?%s" % (self.gcs, urllib.parse.quote(bucket, safe=""), q)

    def put_bytes(self, bucket, name, data, create_only=True):
        """Upload bytes. A create-only upload of an existing object is a
        success (the write is idempotent). Returns False on that 412."""
        url = self._upload_url(bucket, name, create_only)

        def once():
            req = self._request("POST", url, data=data, headers={"Content-Type": "application/octet-stream", "Content-Length": str(len(data))})
            with self.opener.open(req, timeout=120) as r:
                r.read()
            return True

        return self._put(name, once, create_only)

    def put_file(self, bucket, name, path, create_only=True):
        """Upload a file by streaming it from disk."""
        url = self._upload_url(bucket, name, create_only)
        size = os.path.getsize(path)

        def once():
            with open(path, "rb") as f:
                req = self._request("POST", url, data=f, headers={"Content-Type": "application/octet-stream", "Content-Length": str(size)})
                with self.opener.open(req, timeout=600) as r:
                    r.read()
            return True

        return self._put(name, once, create_only)

    def _put(self, name, once, create_only):
        try:
            return retrying("upload of %s" % name, once, self.sleep)
        except HttpStatus as e:
            if e.status == 412 and create_only:
                return False
            raise ShimError("TrainingFailed", "upload of %s failed with HTTP %d" % (name, e.status))

    def get_bytes(self, bucket, name):
        """An object's bytes, or None when it does not exist."""
        url = self._object_url(bucket, name) + "?alt=media"

        def once():
            with self.opener.open(self._request("GET", url), timeout=120) as r:
                return r.read()

        try:
            return retrying("read of %s" % name, once, self.sleep)
        except HttpStatus as e:
            if e.status == 404:
                return None
            raise ShimError("TrainingFailed", "read of %s failed with HTTP %d" % (name, e.status))

    def get_file(self, bucket, name, path):
        """Stream an object to a file and return its sha256, or None when it
        does not exist."""
        url = self._object_url(bucket, name) + "?alt=media"

        def once():
            with self.opener.open(self._request("GET", url), timeout=600) as r:
                return stream_to(r, path)

        try:
            return retrying("read of %s" % name, once, self.sleep)
        except HttpStatus as e:
            if e.status == 404:
                return None
            raise ShimError("TrainingFailed", "read of %s failed with HTTP %d" % (name, e.status))

    def list_names(self, bucket, prefix):
        """Every object name under a prefix, following nextPageToken."""
        names = []
        token = ""
        while True:
            url = "%s/storage/v1/b/%s/o?prefix=%s" % (self.gcs, urllib.parse.quote(bucket, safe=""), urllib.parse.quote(prefix, safe=""))
            if token:
                url += "&pageToken=" + urllib.parse.quote(token, safe="")

            def once():
                with self.opener.open(self._request("GET", url), timeout=60) as r:
                    return json.loads(r.read().decode("utf-8"))

            try:
                body = retrying("list of %s" % prefix, once, self.sleep)
            except HttpStatus as e:
                raise ShimError("TrainingFailed", "list of %s failed with HTTP %d" % (prefix, e.status))
            for item in body.get("items", []) or []:
                names.append(item["name"])
            token = body.get("nextPageToken") or ""
            if not token:
                return names


def stream_to(response, path):
    """Copy a response body to path (through a temporary file) and return
    the sha256 of what was written."""
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    tmp = path + ".part"
    h = hashlib.sha256()
    with open(tmp, "wb") as f:
        for chunk in iter(lambda: response.read(1 << 20), b""):
            h.update(chunk)
            f.write(chunk)
    os.replace(tmp, path)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# Staging
# ---------------------------------------------------------------------------


def safe_relative(path):
    """Refuse a snapshot path that would leave the base directory."""
    parts = path.replace("\\", "/").split("/")
    if not path or path.startswith("/") or any(p in ("", ".", "..") for p in parts):
        raise ShimError("TrainingFailed", "the base snapshot names an unsafe path %r" % path)
    return os.path.join(*parts)


def fetch_blob(cloud, bucket, store_prefix, blob, dest, what):
    """Download one content-addressed data blob and check its digest."""
    digest = str(blob.get("sha256", ""))
    if not digest:
        raise ShimError("DatasetInvalid", "the %s split has no digest" % what)
    got = cloud.get_file(bucket, join_key(store_prefix, "blobs/sha256/" + digest), dest)
    if got is None:
        raise ShimError("DatasetInvalid", "the %s split's blob %s is missing" % (what, digest))
    if got != digest:
        raise ShimError("ChecksumMismatch", "the %s split's blob is %s, not %s" % (what, got, digest))


def fetch_base(base, hf, dest, opener, sleep, tries=3):
    """Download the pinned base snapshot from the Hugging Face resolve
    endpoint, checking every file's digest. No credential is sent."""
    repo = base.get("repo", "")
    revision = base.get("revision", "")
    for entry in base.get("files", []):
        rel = safe_relative(entry["path"])
        url = "%s/%s/resolve/%s/%s" % (hf, repo, urllib.parse.quote(revision, safe=""), urllib.parse.quote(entry["path"], safe="/"))
        target = os.path.join(dest, rel)
        got = None
        for i in range(tries):
            try:
                with opener.open(urllib.request.Request(url), timeout=600) as r:
                    got = stream_to(r, target)
                if got == entry["sha256"]:
                    break
            except (urllib.error.URLError, OSError) as e:
                got = None
                if isinstance(e, urllib.error.HTTPError) and e.code != 429 and e.code < 500:
                    raise ShimError("TrainingFailed", "base file %s: HTTP %d" % (entry["path"], e.code))
            if i < tries - 1:
                sleep(1.0 * (2**i))
        if got is None:
            raise ShimError("TrainingFailed", "base file %s could not be downloaded" % entry["path"])
        if got != entry["sha256"]:
            raise ShimError("ChecksumMismatch", "base file %s is %s, not %s" % (entry["path"], got, entry["sha256"]))


def fetch_checkpoint(cloud, bucket, run_prefix, step, local):
    """Download checkpoints/<step>/ (its manifest and every listed file)."""
    name = step_name(step)
    remote = join_key(run_prefix, "checkpoints/" + name)
    raw = cloud.get_bytes(bucket, remote + "/manifest.json")
    if raw is None:
        raise ShimError("TrainingFailed", "checkpoint %s has no manifest" % name)
    try:
        manifest = json.loads(raw.decode("utf-8"))
        files = manifest["files"]
    except (ValueError, KeyError, TypeError):
        raise ShimError("TrainingFailed", "checkpoint %s has an unreadable manifest" % name)
    out = os.path.join(local, "checkpoints", name)
    os.makedirs(out, exist_ok=True)
    for fname, digest in sorted(files.items()):
        if fname == "manifest.json" or "/" in fname or fname in ("", ".", ".."):
            raise ShimError("TrainingFailed", "checkpoint %s lists an unsafe file %r" % (name, fname))
        got = cloud.get_file(bucket, remote + "/" + fname, os.path.join(out, fname))
        if got is None:
            raise ShimError("TrainingFailed", "checkpoint %s is missing %s" % (name, fname))
        if got != digest:
            raise ShimError("ChecksumMismatch", "checkpoint %s file %s does not match its manifest" % (name, fname))
    with open(os.path.join(out, "manifest.json"), "wb") as f:
        f.write(raw)
    return "checkpoints/" + name


def driver_settings(inputs, attempt, resume_from, resume_dir, base_dir):
    """inputs.driver with the attempt, the resume point and the local paths."""
    cfg = dict(inputs.get("driver") or {})
    cfg["attempt"] = attempt
    cfg["resumeFrom"] = resume_from
    cfg["resumeDir"] = resume_dir
    cfg["python"] = sys.executable
    cfg["driverDir"] = HERE
    if inputs.get("base") is not None:
        base = dict(cfg.get("base") or {})
        base["weights"] = base_dir
        cfg["base"] = base
    return cfg


def driver_env(environ, run=subprocess.run, which=shutil.which, exists=os.path.isdir):
    """The driver's environment. On an NVIDIA driver older than 580 (CUDA
    13's minimum) with the CUDA forward-compatibility libraries present, they
    go first on LD_LIBRARY_PATH."""
    env = dict(environ)
    smi = which("nvidia-smi")
    if not smi:
        log("no nvidia-smi: running without a GPU check")
        return env
    try:
        p = run([smi, "--query-gpu=driver_version", "--format=csv,noheader"], capture_output=True, text=True, timeout=30)
        version = (p.stdout or "").strip().splitlines()[0].strip()
        major = int(version.split(".")[0])
    except (OSError, ValueError, IndexError, subprocess.SubprocessError):
        log("nvidia-smi gave no driver version: running unchanged")
        return env
    if major < 580 and exists(CUDA_COMPAT):
        old = env.get("LD_LIBRARY_PATH", "")
        env["LD_LIBRARY_PATH"] = CUDA_COMPAT + (":" + old if old else "")
        log("NVIDIA driver %s is older than 580: using the CUDA forward-compatibility libraries" % version)
    elif major < 580:
        log("NVIDIA driver %s is older than 580 and %s is absent" % (version, CUDA_COMPAT))
    else:
        log("NVIDIA driver %s" % version)
    return env


# ---------------------------------------------------------------------------
# Mirroring the driver's output
# ---------------------------------------------------------------------------


class Mirror(object):
    """Uploads the driver's new protocol lines, and the files they name, as
    numbered segments."""

    def __init__(self, cloud, bucket, run_prefix, attempt, local, seq=0):
        self.cloud = cloud
        self.bucket = bucket
        self.run = run_prefix
        self.attempt = attempt
        self.local = local
        self.seq = seq
        self.offset = 0
        self.done_step = None
        self.terminal = None

    def key(self, name):
        return join_key(self.run, name)

    def new_lines(self):
        """The complete lines after the offset, and the offset after them."""
        path = os.path.join(self.local, "events.jsonl")
        try:
            with open(path, "rb") as f:
                f.seek(self.offset)
                data = f.read()
        except FileNotFoundError:
            return [], self.offset
        end = data.rfind(b"\n")
        if end < 0:
            return [], self.offset
        chunk = data[: end + 1]
        lines = [ln.decode("utf-8", "replace") for ln in chunk.split(b"\n") if ln.strip()]
        return lines, self.offset + end + 1

    def upload_checkpoint(self, path, step):
        local = os.path.join(self.local, path)
        names = sorted(n for n in os.listdir(local) if n != "manifest.json")
        prefix = "checkpoints/%s/" % step_name(step)
        for n in names:
            self.cloud.put_file(self.bucket, self.key(prefix + n), os.path.join(local, n))
        self.cloud.put_file(self.bucket, self.key(prefix + "manifest.json"), os.path.join(local, "manifest.json"))

    def upload_adapter(self, path):
        local = os.path.join(self.local, path or "adapter")
        files = []
        for n in sorted(os.listdir(local)):
            fp = os.path.join(local, n)
            if not os.path.isfile(fp):
                continue
            self.cloud.put_file(self.bucket, self.key("adapter/" + n), fp, create_only=False)
            files.append({"name": n, "sha256": sha256_file(fp)})
        manifest = json.dumps({"files": files}, sort_keys=True, separators=(",", ":")).encode("utf-8")
        self.cloud.put_bytes(self.bucket, self.key("adapter/manifest.json"), manifest, create_only=False)

    def note(self, ev):
        """Upload what a line names, and remember a terminal line."""
        t = ev.get("type")
        if t == "checkpoint":
            self.upload_checkpoint(ev["path"], ev["step"])
        elif t == "artefact" and ev.get("kind") == "peft":
            self.upload_adapter(ev.get("path", "adapter"))
        elif t == "done":
            self.terminal = "done"
            self.done_step = ev.get("step")
        elif t in ("error", "cancelled") and self.terminal is None:
            self.terminal = t

    def flush(self):
        """One cycle: the files the new lines name, then the lines as one
        segment. On a failure the offset stays, so the next cycle repeats the
        (idempotent) uploads."""
        lines, end = self.new_lines()
        if not lines:
            return
        for line in lines:
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            if isinstance(ev, dict):
                self.note(ev)
        self.put_segment(lines)
        self.offset = end

    def put_segment(self, lines):
        body = ("\n".join(lines) + "\n").encode("utf-8")
        name = "attempt-%d/events/%s.jsonl" % (self.attempt, segment_name(self.seq))
        self.cloud.put_bytes(self.bucket, self.key(name), body)
        self.seq += 1


def next_segment(cloud, bucket, run_prefix, attempt):
    """The first free segment number of an attempt (0 for a new attempt; a
    restarted container continues after its predecessor's segments)."""
    prefix = join_key(run_prefix, "attempt-%d/events/" % attempt)
    top = -1
    for n in cloud.list_names(bucket, prefix):
        base = n[len(prefix) :]
        if base.endswith(".jsonl") and base[:-6].isdigit():
            top = max(top, int(base[:-6]))
    return top + 1


# ---------------------------------------------------------------------------
# The attempt
# ---------------------------------------------------------------------------


def parse_args(argv):
    out = {}
    i = 0
    keys = ("--inputs", "--run", "--attempt", "--resume-from")
    while i < len(argv):
        if argv[i] in keys and i + 1 < len(argv):
            out[argv[i][2:]] = argv[i + 1]
            i += 2
        else:
            i += 1
    for k in ("inputs", "run"):
        if k not in out:
            raise SystemExit("usage: sky_cloud.py --inputs gs://... --run gs://... --attempt <a> --resume-from <step>")
    return out["inputs"], out["run"], int(out.get("attempt", "0")), int(out.get("resume-from", "0"))


class Shim(object):
    """One attempt. `clock` and `sleep` are injectable for tests."""

    def __init__(self, argv, environ=None, clock=time.time, sleep=time.sleep):
        self.environ = dict(os.environ if environ is None else environ)
        self.inputs_url, self.run_url, self.attempt, self.resume = parse_args(argv)
        self.clock = clock
        self.sleep = sleep
        self.flush_s = float(self.environ.get("SKY_CLOUD_FLUSH_S", "5") or 5)
        self.work = self.environ.get("SKY_CLOUD_WORK", "") or "/tmp/sky-run"
        self.local = os.path.join(self.work, "run")
        self.base_dir = os.path.join(self.work, "base")
        self.hf = (self.environ.get("SKY_AI_HF_ENDPOINT", "") or "https://huggingface.co").rstrip("/")
        self.cloud = Cloud(self.environ, clock, sleep)
        self.bucket, self.run = split_gs(self.run_url)
        self.term = False
        self.mirror = None

    def on_signal(self, signum, frame):
        self.term = True

    def key(self, name):
        return join_key(self.run, name)

    def write_hello(self):
        body = {"jobId": self.environ.get("CLOUD_ML_JOB_ID", ""), "attempt": self.attempt, "at": int(self.clock() * 1000)}
        data = json.dumps(body, sort_keys=True, separators=(",", ":")).encode("utf-8")
        self.cloud.put_bytes(self.bucket, self.key("attempt-%d/hello.json" % self.attempt), data)

    def stage(self):
        """Inputs, data, base and checkpoint on the local disk; driver.json."""
        os.makedirs(os.path.join(self.local, "data"), exist_ok=True)
        ib, iname = split_gs(self.inputs_url)
        raw = self.cloud.get_bytes(ib, iname)
        if raw is None:
            raise ShimError("TrainingFailed", "the run's inputs are missing at %s" % self.inputs_url)
        try:
            inputs = json.loads(raw.decode("utf-8"))
            data = inputs["data"]
        except (ValueError, KeyError, TypeError):
            raise ShimError("TrainingFailed", "the run's inputs.json is unreadable")
        sbucket, sprefix = split_gs(inputs.get("store") or ("gs://" + self.bucket))
        fetch_blob(self.cloud, sbucket, sprefix, data.get("train") or {}, os.path.join(self.local, "data", "train.jsonl"), "train")
        fetch_blob(self.cloud, sbucket, sprefix, data.get("valid") or {}, os.path.join(self.local, "data", "valid.jsonl"), "eval")
        if inputs.get("base") is not None:
            opener = make_opener(bool(self.environ.get("SKY_AI_HF_ENDPOINT")), redirects=True)
            fetch_base(inputs["base"], self.hf, self.base_dir, opener, self.sleep)
        resume_dir = ""
        if self.resume > 0:
            resume_dir = fetch_checkpoint(self.cloud, self.bucket, self.run, self.resume, self.local)
        cfg = driver_settings(inputs, self.attempt, self.resume, resume_dir, self.base_dir)
        with open(os.path.join(self.local, "driver.json"), "w", encoding="utf-8") as f:
            json.dump(cfg, f, sort_keys=True, separators=(",", ":"))

    def start_driver(self, env):
        cmd = [sys.executable, "-I", "-B", os.path.join(HERE, "sky_train.py"), "--run", self.local, "--launch", "cloud-%d" % self.attempt]
        out = open(os.path.join(self.local, "events.jsonl"), "ab")
        err = open(os.path.join(self.local, "stderr.log"), "ab")
        try:
            return subprocess.Popen(cmd, stdout=out, stderr=err, stdin=subprocess.DEVNULL, cwd=self.local, env=env)
        finally:
            out.close()
            err.close()

    def try_flush(self):
        try:
            self.mirror.flush()
            return True
        except ShimError as e:
            log("upload deferred: " + e.message)
            return False

    def watch(self, proc):
        """Mirror the driver until it exits; forward a cancel. Returns the
        driver's exit code."""
        cancel_seen = False
        forwarded = False
        deadline = None
        while True:
            rc = proc.poll()
            self.try_flush()
            if rc is not None:
                return rc
            if not cancel_seen:
                try:
                    if self.cloud.get_bytes(self.bucket, self.key("cancel")) is not None:
                        cancel_seen = True
                        open(os.path.join(self.local, "cancel"), "w").close()
                        log("cancel requested")
                except ShimError as e:
                    log("cancel check deferred: " + e.message)
            if (cancel_seen or self.term) and not forwarded:
                forwarded = True
                proc.send_signal(signal.SIGTERM)
            if self.term and deadline is None:
                deadline = self.clock() + TERM_GRACE_S
            if deadline is not None and self.clock() > deadline:
                log("the driver did not stop in %d s: killing it" % TERM_GRACE_S)
                proc.kill()
                return proc.wait()
            self.sleep(self.flush_s)

    def final_flush(self):
        for i in range(3):
            if self.try_flush():
                return True
            self.sleep(2.0 * (i + 1))
        return False

    def finish(self, rc):
        """Write the end of the attempt and return the shim's exit code."""
        m = self.mirror
        if m.terminal == "done":
            body = json.dumps({"attempt": self.attempt, "step": m.done_step}, sort_keys=True, separators=(",", ":")).encode("utf-8")
            self.cloud.put_bytes(self.bucket, self.key("done.json"), body, create_only=False)
            return 0
        if m.terminal == "cancelled":
            return 0
        if m.terminal == "error":
            return 1
        tail = b""
        try:
            with open(os.path.join(self.local, "stderr.log"), "rb") as f:
                f.seek(0, os.SEEK_END)
                f.seek(max(0, f.tell() - STDERR_TAIL))
                tail = f.read()
        except OSError:
            pass
        self.cloud.put_bytes(self.bucket, self.key("attempt-%d/stderr.txt" % self.attempt), tail, create_only=False)
        code = rc if rc > 0 else (128 - rc if rc < 0 else 1)
        log("the driver ended with no terminal line (exit %d)" % code)
        return code

    def fail_early(self, err):
        """A failure before the driver starts: one protocol error segment."""
        log("%s: %s" % (err.code, err.message))
        line = json.dumps({"v": 1, "type": "error", "code": err.code, "message": err.message}, sort_keys=True, separators=(",", ":"))
        try:
            self.mirror.put_segment([line])
        except ShimError as e:
            log("the error segment could not be written: " + e.message)
        return 1

    def main(self):
        self.mirror = Mirror(self.cloud, self.bucket, self.run, self.attempt, self.local)
        try:
            self.write_hello()
            self.mirror.seq = next_segment(self.cloud, self.bucket, self.run, self.attempt)
            self.stage()
            env = driver_env(self.environ)
            proc = self.start_driver(env)
        except ShimError as e:
            return self.fail_early(e)
        log("attempt %d started from step %d" % (self.attempt, self.resume))
        rc = self.watch(proc)
        if not self.final_flush():
            log("the last protocol lines could not be uploaded")
            return 1
        try:
            return self.finish(rc)
        except ShimError as e:
            log(e.message)
            return 1


def main(argv, environ=None, clock=time.time, sleep=time.sleep):
    try:
        shim = Shim(argv, environ, clock, sleep)
    except ShimError as e:
        log("%s: %s" % (e.code, e.message))
        return 1
    try:
        signal.signal(signal.SIGTERM, shim.on_signal)
        signal.signal(signal.SIGINT, shim.on_signal)
    except ValueError:
        pass  # not the main thread (a test): no signal handlers
    return shim.main()


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
