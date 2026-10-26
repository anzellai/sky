"""The parts of the Sky training driver that every backend shares.

Standard library only. The Go runtime launches the driver as a detached
process with its standard output redirected to `<run>/events.jsonl`, so every
line this module writes to stdout is one protocol event. The Sky side reads the
file from an offset and decodes the lines with `Std.Ai.Train.decodeEvent`.

Protocol (one JSON object per line, keys sorted, "v": 1):

    hello       backend, toolchain, versions, attempt, resumeFrom, totalSteps
    progress    step, total, loss, learningRate, tokensPerSecond, epoch
    metric      step, name, value
    checkpoint  step, path
    artefact    kind, path, sha256
    cancelled   step
    done        step, trainLoss, valLoss
    error       code, message

A checkpoint is written to a temporary directory, every file is flushed to
disk, a manifest of SHA-256 digests is written last, and the directory is
renamed into place before its line is written. A reader that sees the line
therefore sees a complete checkpoint, and the Go side re-hashes the manifest
before it resumes from one. Checkpoints hold safetensors and JSON only, never
a pickle.
"""

import hashlib
import json
import math
import os
import random
import struct
import sys

PROTOCOL_VERSION = 1

# Where protocol lines go. `claim_stdout` points it at the real standard
# output and sends everything else written to standard output (a library's
# print, a C extension's message) to standard error, so the protocol file
# holds protocol lines only.
_out = None


def claim_stdout():
    global _out
    _out = os.fdopen(os.dup(1), "w", buffering=1)
    os.dup2(2, 1)
    sys.stdout = sys.stderr


def emit(event_type, **fields):
    """Write one protocol line and flush it."""
    obj = {"v": PROTOCOL_VERSION, "type": event_type}
    for k, val in fields.items():
        if isinstance(val, float) and not math.isfinite(val):
            val = None
        obj[k] = val
    line = json.dumps(obj, sort_keys=True, separators=(",", ":"))
    out = _out or sys.stdout
    out.write(line + "\n")
    out.flush()


# ---------------------------------------------------------------------------
# safetensors, read and written with the standard library
# ---------------------------------------------------------------------------

DTYPES = {"F32": ("f", 4), "F16": ("e", 2), "I64": ("q", 8), "I32": ("i", 4)}


def write_safetensors(path, tensors, metadata=None):
    """Write tensors to a safetensors file.

    tensors maps a name to (dtype, shape, data), where data is the raw
    little-endian bytes of the tensor in row-major order.
    """
    header = {}
    offset = 0
    names = sorted(tensors)
    for name in names:
        dtype, shape, data = tensors[name]
        if dtype not in DTYPES:
            raise ValueError("unsupported dtype %s" % dtype)
        want = DTYPES[dtype][1]
        for d in shape:
            want *= d
        if len(data) != want:
            raise ValueError("tensor %s: %d bytes for shape %s" % (name, len(data), shape))
        header[name] = {"dtype": dtype, "shape": list(shape), "data_offsets": [offset, offset + len(data)]}
        offset += len(data)
    if metadata:
        header["__metadata__"] = {str(k): str(v) for k, v in metadata.items()}
    raw = json.dumps(header, sort_keys=True, separators=(",", ":")).encode("utf-8")
    raw += b" " * ((8 - len(raw) % 8) % 8)
    with open(path, "wb") as f:
        f.write(struct.pack("<Q", len(raw)))
        f.write(raw)
        for name in names:
            f.write(tensors[name][2])
        f.flush()
        os.fsync(f.fileno())


def read_safetensors(path):
    """Read a safetensors file: (tensors, metadata), tensors as in write."""
    with open(path, "rb") as f:
        (n,) = struct.unpack("<Q", f.read(8))
        if n == 0 or n > 100 * 1024 * 1024:
            raise ValueError("%s: bad safetensors header length" % path)
        header = json.loads(f.read(n).decode("utf-8"))
        body = f.read()
    meta = header.pop("__metadata__", {}) or {}
    out = {}
    for name, info in header.items():
        a, b = info["data_offsets"]
        out[name] = (info["dtype"], tuple(info["shape"]), body[a:b])
    return out, meta


def f32_bytes(values):
    return struct.pack("<%df" % len(values), *values)


def f32_values(data):
    return list(struct.unpack("<%df" % (len(data) // 4), data))


# ---------------------------------------------------------------------------
# files and checkpoints
# ---------------------------------------------------------------------------


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fsync_dir(path):
    try:
        fd = os.open(path, os.O_RDONLY)
    except OSError:
        return
    try:
        os.fsync(fd)
    except OSError:
        pass
    finally:
        os.close(fd)


def write_json(path, value):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(value, f, sort_keys=True, separators=(",", ":"))
        f.flush()
        os.fsync(f.fileno())


def checkpoint_name(step):
    return "%07d" % step


def write_checkpoint(run_dir, step, save, state):
    """Write checkpoints/<step>/ atomically and return its relative path.

    save(dir) writes the backend's files (adapter.safetensors and
    optimizer.safetensors) into dir. state is the JSON-safe training state.
    """
    root = os.path.join(run_dir, "checkpoints")
    os.makedirs(root, exist_ok=True)
    name = checkpoint_name(step)
    final = os.path.join(root, name)
    if os.path.isdir(final):
        return os.path.join("checkpoints", name)
    tmp = os.path.join(root, "tmp-" + name + "-" + str(os.getpid()))
    if os.path.isdir(tmp):
        for f in os.listdir(tmp):
            os.remove(os.path.join(tmp, f))
    else:
        os.makedirs(tmp)
    save(tmp)
    write_json(os.path.join(tmp, "state.json"), dict(state, step=step))
    files = {}
    for f in sorted(os.listdir(tmp)):
        files[f] = sha256_file(os.path.join(tmp, f))
    write_json(os.path.join(tmp, "manifest.json"), {"step": step, "files": files})
    fsync_dir(tmp)
    os.rename(tmp, final)
    fsync_dir(root)
    return os.path.join("checkpoints", name)


def verify_checkpoint(path):
    """Whether a checkpoint directory's manifest matches its files."""
    try:
        with open(os.path.join(path, "manifest.json"), encoding="utf-8") as f:
            manifest = json.load(f)
        for name, digest in manifest["files"].items():
            if sha256_file(os.path.join(path, name)) != digest:
                return False
        return True
    except (OSError, ValueError, KeyError):
        return False


# ---------------------------------------------------------------------------
# data order
# ---------------------------------------------------------------------------


def epoch_order(n, seed, epoch):
    """The permutation of n rows for one epoch, from the seed alone."""
    order = list(range(n))
    random.Random(seed * 1000003 + epoch).shuffle(order)
    return order


def batch_indices(n, batch, seed, step):
    """The row indices of optimiser step `step` (counted from 0).

    The order depends only on (n, batch, seed, step), so a run resumed at step
    s reads exactly the batches the first run would have read.
    """
    if n <= 0:
        return []
    out = []
    cache = {}
    start = step * batch
    for k in range(batch):
        q = start + k
        epoch, i = divmod(q, n)
        if epoch not in cache:
            cache[epoch] = epoch_order(n, seed, epoch)
        out.append(cache[epoch][i])
    return out


def total_steps(cfg, n_train):
    """Optimiser steps for the job: maxSteps when set, else whole epochs."""
    if cfg.get("maxSteps", 0) > 0:
        return int(cfg["maxSteps"])
    batch = max(1, cfg["batch"]["perDevice"] * cfg["batch"]["accumulate"])
    return max(1, int(math.ceil(cfg["epochs"] * n_train / float(batch))))


def read_rows(path):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


# ---------------------------------------------------------------------------
# PEFT
# ---------------------------------------------------------------------------


def peft_config(base, rank, alpha, targets):
    return {
        "base_model_name_or_path": base,
        "bias": "none",
        "fan_in_fan_out": False,
        "inference_mode": True,
        "lora_alpha": alpha,
        "lora_dropout": 0.0,
        "peft_type": "LORA",
        "r": rank,
        "target_modules": sorted(set(t.split(".")[-1] for t in targets)),
        "task_type": "CAUSAL_LM",
    }
