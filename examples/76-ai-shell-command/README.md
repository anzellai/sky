# 76 · A request in, one terminal command out

This example trains a small local model to turn a request in plain words into
one macOS or Linux terminal command. "find the file xyz.csv" becomes
`find . -name xyz.csv`, or `fd -g xyz.csv` where `fd` is installed. A firewall
decides what may run. The model never does.

> **Excluded from CI.** No CI job builds, trains, evaluates or runs this
> example, because training takes too long for CI (user rule,
> [anzellai/sky#299](https://github.com/anzellai/sky/issues/299)). Every gate
> that enumerates `examples/` skips it by name, and
> `rust/crates/xtask/tests/ci_excluded_examples.rs` fails the build if a
> workflow or a gate would touch it. Run it yourself, on your own machine.

```bash
sky build src/Main.sky
./sky-out/app train                          # fetch the data, freeze it, train the adapter
./sky-out/app eval                           # score the baseline and the fine-tuned model
./sky-out/app ask "how many lines are in notes.txt"
./sky-out/app data                           # show the training rows, without training
sky test tests/ShellCommandTest.sky          # the firewall, the cleaner, the eval harness
```

## The result

Each eval case is a request in words the training data never uses. The model's
command and a reference command each run in a fresh copy of a fixture folder.
The case passes when both give the same output and leave the folder in the
same state. The text of the command does not matter: `fd -e md` passes where
the reference is `find . -type f -name '*.md'`.

| Platform | Baseline (base model, few-shot) | Fine-tuned (adapter, zero-shot) |
|---|---|---|
| macOS (BSD tools, `fd` and `rg` installed) | 10 of 33 (30 %) | 17 of 33 (51 %) |
| Linux, Debian (GNU tools, no `fd`, no `rg`) | 13 of 33 (39 %) | 22 of 33 (66 %) |

Measured at commit RESULT_COMMIT on the Mac (Apple silicon, 16 GB) and on a throwaway Debian 12 VM (e2-standard-4). Both models are Qwen2.5 0.5B Instruct (Q4_0)
on the managed local engine, with greedy decoding. The baseline sees six
solved examples in its prompt. The fine-tuned model sees none. The adapter was
trained once, on a Mac, and the Linux eval loads the same adapter file.

## What `ask` does

```
$ ./sky-out/app ask "delete every tmp file"
Platform: macOS (BSD tools), fd installed, rg installed
Model: fine-tuned adapter
Command: fd -e tmp -x rm
Firewall: needs approval (fd runs a command on each file)
Run it? [y/N]
```

1. It detects the platform: `uname -s`, and whether `fd` and `rg` are on the
   tool `PATH`. The platform is part of the prompt.
2. The model proposes one command.
3. `Firewall.propose` wraps the command in a typed `Std.Ai.Policy.Action`, and
   `Firewall.firewall` (a pure function) decides:

   | Decision | Commands |
   |---|---|
   | `Allow` | read-only: `find`, `fd`, `grep`, `rg`, `ls`, `wc`, `head`, `tail`, `sort`, `cut`, `stat`, `du` ... |
   | `NeedsApproval` | writes or deletes: `mkdir`, `cp`, `mv`, `rm`, `touch`, `sed -i`, `find -delete`, `fd -x`, a `>` redirect, and any program the firewall does not know |
   | `Deny` | `sudo`, a download piped into a shell (`curl ... \| sh`), `rm -rf /`, a write outside the folder (`/`, `~`, `..`), `dd`, `mkfs`, `chown`, a fork bomb ... |

4. `Policy.gate` runs the command only on `Allow`. On `NeedsApproval` it asks
   you first. A `Deny` never runs, whoever approves.

The model is never the authorisation boundary. It can propose
`rm -rf ~/*` (the training data even teaches it to, when asked), and the
firewall refuses it.

## Training

`train` builds the dataset, freezes it with `Std.Ai.Dataset`, and trains a
QLoRA adapter with `Std.Ai.Train`:

- **The template grammar** (`src/Grammar.sky`): 47 tasks (find by name, count
  lines, file size, replace in place, delete a folder ...) x their phrasings x
  4 platforms (macOS or Linux, with or without `fd` and `rg`). Each pass over
  the phrasings asks in another register ("please ...", "can you ...", "...?").
  The command follows the platform: `stat -f %z` on macOS, `stat -c %s` on
  Linux, `sed -i ''` against `sed -i`, `md5 -q` against `md5sum`, `fd` and `rg`
  only where they are installed. A task whose command differs between the two
  systems gets three times the rows, so the model learns to read the platform
  line.
- **tldr-pages** (`src/Tldr.sky`): 23 pages at one pinned commit, each file
  checked against its SHA-256. Each example becomes a row: its description is
  the request and its command (placeholders filled in) is the answer. A
  platform page (`osx/stat`, `linux/sed`) wins over the `common` page there.
- **The job** (`src/Model.sky`): Qwen2.5 0.5B Instruct, 4-bit, rank 16 on the
  attention projections of the last 16 layers, 1200 steps of 8 rows of up to
  192 tokens, seed 7, a memory cap of 2.5 GB. The adapter is converted to GGUF
  for the local engine.

On a Mac with Apple silicon the native trainer (`sky-trainer`, MLX) runs it.
The run measured above took about 29 minutes on an Apple M-series
Mac with 16 GB, with a peak memory footprint of 0.79 GB (the cap is 2.5 GB). The trainer runs
detached: stop `train` with Ctrl-C, run it again, and it follows the same run.

Nothing large goes into git. The tldr pages are cached under `.sky/tldr/`, and
the dataset, the run, the adapter and the eval reports are under `.sky/ai/`.
`train` records the adapter in `.sky/ai/shell-adapter.txt` (its path and
SHA-256), and `eval` and `ask` load it from there.

## Evaluation

`eval` runs 33 cases per platform (`src/Cases.sky`): 25 that only read and 8
that write. Each run of a command:

- runs in a fresh fixture folder (`src/Fixture.sky`) with every file's time
  fixed, so a case that reads a modification time is stable;
- runs with a fixed environment: the system tools first on `PATH` (so a Mac
  answers with BSD tools even when GNU tools come first on your own `PATH`),
  `HOME` set to the fixture, the `C` locale;
- stops after 10 seconds (`tail -f`) or 200 kB of output.

A case passes when the exit code, the output and the folder's state (each
path, with the SHA-256 of each file) match the reference's. The output is
compared line by line after trimming, and as a set for a list of files, whose
order depends on the tool.

The firewall applies here too. A `Deny` never runs, and the case fails. A
`NeedsApproval` command runs, because the eval approves it: it runs in a
throwaway folder, and the firewall has already refused anything that reaches
outside it.

`eval` writes its report to `.sky/ai/eval-macos.json` or `eval-linux.json`.
`eval baseline` and `eval tuned` score one model.

### On Linux

The `Mlx` trainer needs Apple silicon, so train on a Mac (or on Linux with an
NVIDIA GPU, where `Unsloth` runs). To evaluate on Linux, copy the adapter
file `.sky/ai/shell-adapter.gguf` and `.sky/ai/shell-adapter.txt` to the same
paths on the Linux machine, and run `eval` there. The engine and the model are
fetched for the host. The engine's Linux build needs `libgomp` (on Debian,
`apt-get install libgomp1`), as `docs/ai/local.md` says. A Linux binary can be
built on a Mac from the generated Go: `cd sky-out && GOOS=linux GOARCH=amd64
CGO_ENABLED=0 go build -o app-linux .`

## Limits

A 0.5B model handles a single command or a short pipeline, not a script. The
eval is 33 cases on one fixture, a check that the adapter learnt the task and
the platform, not a benchmark of shell knowledge.

## Files

| File | What it holds |
|---|---|
| `src/Main.sky` | the CLI: `ask`, `train`, `eval`, `data` |
| `src/Firewall.sky` | the command classifier and the `Std.Ai.Policy` firewall |
| `src/Prompt.sky` | the platform line, the prompts and the answer cleaner |
| `src/Grammar.sky` | the template grammar (task x tool x platform) |
| `src/Tldr.sky` | the pinned tldr-pages and their parser |
| `src/Model.sky` | the dataset, the training job, the adapter and the two models |
| `src/Cases.sky` | the eval cases and the output comparison |
| `src/Fixture.sky` | the fixture folder and its state |
| `src/Judge.sky` | one case: run the reference and the proposal, compare |
| `src/Shell.sky` | run a command with a time limit, detect the platform |
| `tests/ShellCommandTest.sky` | the firewall's decisions, the cleaner, the tldr placeholders, every reference against itself |

## Attribution

The training data includes examples from
[tldr-pages](https://github.com/tldr-pages/tldr), copyright (c) 2014 the
tldr-pages team and contributors, licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). The pages are read
at commit `25f7dbe55d03efa3ca1a27c21eea571cb86bd601`, and each example is
changed: its placeholders are filled in and its description is used as a
request. The base model, Qwen2.5 0.5B Instruct, is licensed under Apache-2.0.
