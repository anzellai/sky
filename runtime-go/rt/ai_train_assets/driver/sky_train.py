"""The Sky training driver.

    python -I -B sky_train.py --run <run dir> --launch <token>

The Go runtime (ai_train.go) writes `<run>/driver.json`, stages the data under
`<run>/data/`, and starts this program detached, with its standard output
redirected to `<run>/events.jsonl` and its standard error to
`<run>/stderr.log`. The program trains, writes checkpoints, exports the adapter
in the PEFT layout under `<run>/adapter/`, and reports every step as a protocol
line (sky_protocol.py). It never reads the network: base weights are a local
directory, and the Hugging Face libraries run offline.

A SIGTERM, or a `<run>/cancel` file, stops the run at the next step boundary:
the driver writes a checkpoint and a `cancelled` line and exits 0.

The `--launch` token is not read here. It is on the command line so that the
Go side can tell this process from an unrelated one that reuses its pid.
"""

import json
import os
import signal
import sys
import time
import traceback

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import sky_protocol as P  # noqa: E402


class DriverError(Exception):
    """A failure with a Std.Ai.Failure code."""

    def __init__(self, code, message):
        Exception.__init__(self, message)
        self.code = code
        self.message = message


_cancel = {"flag": False}


def _on_term(signum, frame):
    _cancel["flag"] = True


def cancel_requested(run_dir):
    return _cancel["flag"] or os.path.exists(os.path.join(run_dir, "cancel"))


def load_backend(name):
    try:
        if name == "unsloth":
            from backends import unsloth

            return unsloth.UnslothBackend()
    except ImportError as e:
        raise DriverError("ToolchainFailed", "the %s toolchain cannot import %s" % (name, e.name or e))
    except RuntimeError as e:
        text = str(e)
        if text.startswith("NoAccelerator:"):
            raise DriverError("NoAccelerator", text[len("NoAccelerator:") :].strip())
        raise
    raise DriverError("TrainingFailed", "unknown backend %r" % name)


def parse_args(argv):
    out = {}
    i = 0
    while i < len(argv):
        if argv[i] in ("--run", "--launch") and i + 1 < len(argv):
            out[argv[i][2:]] = argv[i + 1]
            i += 2
        else:
            i += 1
    if "run" not in out:
        raise SystemExit("usage: sky_train.py --run <dir> --launch <token>")
    return out


def run(run_dir, backend=None, now=time.time):
    """Train one attempt. Returns the process exit code."""
    with open(os.path.join(run_dir, "driver.json"), encoding="utf-8") as f:
        cfg = json.load(f)
    attempt = int(cfg.get("attempt", 0))
    start = int(cfg.get("resumeFrom", 0))
    try:
        if backend is None:
            backend = load_backend(cfg["backend"])
        train = P.read_rows(os.path.join(run_dir, "data", "train.jsonl"))
        valid = P.read_rows(os.path.join(run_dir, "data", "valid.jsonl"))
        if not train:
            raise DriverError("DatasetInvalid", "the train split is empty")
        if not valid:
            raise DriverError("DatasetInvalid", "the eval split is empty")
        total = P.total_steps(cfg, len(train))
        backend.prepare(cfg, train, valid)
        P.emit(
            "hello",
            backend=cfg["backend"],
            toolchain=cfg.get("toolchain", ""),
            versions=backend.versions(),
            attempt=attempt,
            resumeFrom=start,
            totalSteps=total,
            pid=os.getpid(),
        )
        if start > 0:
            ckpt = os.path.join(run_dir, cfg.get("resumeDir", P.checkpoint_name(start)))
            if not P.verify_checkpoint(ckpt):
                raise DriverError("TrainingFailed", "checkpoint %s does not match its manifest" % ckpt)
            backend.load(ckpt)
        batch = max(1, cfg["batch"]["perDevice"] * cfg["batch"]["accumulate"])
        accumulate = max(1, cfg["batch"]["accumulate"])
        every = int(cfg.get("checkpointEvery", 0))
        eval_every = int(cfg.get("evalEvery", 0))
        crash_at = int(cfg.get("fakeCrashAt", 0))
        last_ckpt = start
        last_loss = None
        lr = float(cfg["learningRate"])
        epoch_len = max(1, (len(train) + batch - 1) // batch)

        def checkpoint(done):
            path = P.write_checkpoint(run_dir, done, backend.save, {"loss": last_loss, "attempt": attempt})
            P.emit("checkpoint", step=done, path=path)

        for step in range(start, total):
            if cancel_requested(run_dir):
                if step > last_ckpt:
                    checkpoint(step)
                P.emit("cancelled", step=step)
                return 0
            t0 = now()
            indices = P.batch_indices(len(train), batch, int(cfg["seed"]), step)
            loss, tokens = backend.train_step(indices, accumulate)
            done = step + 1
            last_loss = float(loss)
            elapsed = max(now() - t0, 1e-6)
            P.emit(
                "progress",
                step=done,
                total=total,
                loss=last_loss,
                learningRate=lr,
                tokensPerSecond=round(tokens / elapsed, 3),
                epoch=step // epoch_len,
            )
            if eval_every > 0 and done % eval_every == 0 and done < total:
                P.emit("metric", step=done, name="val_loss", value=float(backend.evaluate()))
            if every > 0 and done % every == 0 and done < total:
                checkpoint(done)
                last_ckpt = done
            if crash_at > 0 and attempt == 0 and done == crash_at:
                # A test's scripted crash: no terminal line, no cleanup, the
                # way a SIGKILL or a power cut ends a trainer.
                os._exit(137)
        val = float(backend.evaluate())
        P.emit("metric", step=total, name="val_loss", value=val)
        out = os.path.join(run_dir, "adapter")
        tmp = os.path.join(run_dir, "adapter.tmp-" + str(os.getpid()))
        os.makedirs(tmp, exist_ok=True)
        backend.export_peft(tmp)
        for f in os.listdir(tmp):
            fp = os.path.join(tmp, f)
            with open(fp, "rb+") as h:
                os.fsync(h.fileno())
        if os.path.isdir(out):
            for f in os.listdir(out):
                os.remove(os.path.join(out, f))
            os.rmdir(out)
        os.rename(tmp, out)
        P.fsync_dir(run_dir)
        weights = os.path.join(out, "adapter_model.safetensors")
        P.emit("artefact", kind="peft", path="adapter", sha256=P.sha256_file(weights))
        P.emit("done", step=total, trainLoss=last_loss, valLoss=val)
        return 0
    except DriverError as e:
        P.emit("error", code=e.code, message=e.message)
        return 1
    except MemoryError:
        P.emit("error", code="ModelTooLarge", message="the trainer ran out of memory")
        return 1
    except Exception as e:  # noqa: BLE001 - every failure becomes a typed line
        tail = traceback.format_exc()[-1500:]
        sys.stderr.write(tail)
        P.emit("error", code="TrainingFailed", message="%s: %s" % (type(e).__name__, e))
        return 1


def selfcheck(backend):
    """The toolchain's self-check: one `hello` line with the versions the
    toolchain carries. Unsloth reads its package metadata only (importing
    Unsloth needs a GPU, and the install is checked on machines without one)."""
    import importlib.metadata as md
    import platform

    versions = {"python": platform.python_version()}
    if backend == "unsloth":
        versions["unsloth"] = md.version("unsloth")
        versions["torch"] = md.version("torch")
    P.emit("hello", backend=backend, versions=versions, selfcheck=True)
    return 0


def main(argv):
    if len(argv) == 2 and argv[0] == "--selfcheck":
        return selfcheck(argv[1])
    args = parse_args(argv)
    P.claim_stdout()
    signal.signal(signal.SIGTERM, _on_term)
    if hasattr(signal, "SIGINT"):
        signal.signal(signal.SIGINT, _on_term)
    return run(args["run"])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
