"""The Unsloth backend: LoRA and QLoRA on an NVIDIA GPU with Unsloth core
(Apache-2.0). The AGPL Unsloth Studio is never installed (the lock policy test
refuses it).

`FastLanguageModel.from_pretrained` loads the local base snapshot (a
`unsloth/...-bnb-4bit` snapshot makes it QLoRA) and `get_peft_model` adds the
adapters. The loop is the driver's own, as on MLX, so the two backends share
the data order, the protocol and the checkpoint format: the adapter weights and
the optimiser state are safetensors (never `torch.save`, which is a pickle),
and a resume restores both.

Everything that touches torch or Unsloth goes through an `api` object, so the
driver's part is tested without a GPU (test_unsloth_fixture.py injects a
recording fake). `RealApi` is the only code that imports them.
"""

import json
import os
import platform

import sky_protocol as P
import sky_rows as R


class RealApi(object):
    """The calls into torch and Unsloth, on a real CUDA machine."""

    def __init__(self):
        import torch

        if not torch.cuda.is_available():
            raise RuntimeError("NoAccelerator: Unsloth needs a CUDA GPU and torch sees none")
        from unsloth import FastLanguageModel

        self.torch = torch
        self.flm = FastLanguageModel

    def versions(self):
        import importlib.metadata as md

        return {
            "torch": self.torch.__version__,
            "unsloth": md.version("unsloth"),
            "cuda": str(self.torch.version.cuda),
        }

    def load(self, weights, seq_len, load_in_4bit):
        return self.flm.from_pretrained(
            model_name=weights,
            max_seq_length=seq_len,
            load_in_4bit=load_in_4bit,
            dtype=None,
            local_files_only=True,
            trust_remote_code=False,
        )

    def peft(self, model, rank, alpha, targets, seed, layers):
        return self.flm.get_peft_model(
            model,
            r=rank,
            lora_alpha=alpha,
            lora_dropout=0.0,
            bias="none",
            target_modules=sorted(set(t.split(".")[-1] for t in targets)),
            layers_to_transform=layers,
            random_state=seed,
            use_gradient_checkpointing="unsloth",
        )

    def optimizer(self, model, lr):
        params = [p for p in model.parameters() if p.requires_grad]
        return self.torch.optim.AdamW(params, lr=lr, weight_decay=0.0)

    def step(self, model, opt, batches):
        torch = self.torch
        model.train()
        total = 0.0
        for ids, labels in batches:
            ids_t = torch.tensor(ids, device="cuda")
            labels_t = torch.tensor(labels, device="cuda")
            out = model(input_ids=ids_t, labels=labels_t)
            (out.loss / len(batches)).backward()
            total += float(out.loss.item())
        opt.step()
        opt.zero_grad(set_to_none=True)
        return total / len(batches)

    def eval_loss(self, model, ids, labels):
        torch = self.torch
        model.eval()
        with torch.no_grad():
            out = model(input_ids=torch.tensor(ids, device="cuda"), labels=torch.tensor(labels, device="cuda"))
        return float(out.loss.item())

    def lora_tensors(self, model):
        """name -> (shape, float32 bytes) of every trainable LoRA matrix, with
        PEFT names (`base_model.model....lora_A.weight`)."""
        out = {}
        for name, p in model.named_parameters():
            if p.requires_grad:
                t = p.detach().float().cpu().contiguous()
                out[name.replace(".default", "")] = (tuple(t.shape), t.numpy().astype("<f4").tobytes())
        return out

    def load_lora_tensors(self, model, tensors):
        import numpy as np

        params = dict(model.named_parameters())
        for name, p in params.items():
            key = name.replace(".default", "")
            if key in tensors:
                shape, data = tensors[key]
                arr = np.frombuffer(data, dtype="<f4").reshape(shape)
                with self.torch.no_grad():
                    p.copy_(self.torch.from_numpy(arr.copy()).to(p.device, p.dtype))

    def optimizer_tensors(self, opt):
        """The AdamW state as (tensors, JSON metadata), never a pickle."""
        sd = opt.state_dict()
        tensors = {}
        for idx, st in sd["state"].items():
            for k, v in st.items():
                t = v.detach().float().cpu().contiguous() if hasattr(v, "detach") else self.torch.tensor([float(v)])
                tensors["state.%s.%s" % (idx, k)] = (tuple(t.shape), t.numpy().astype("<f4").tobytes())
        return tensors, json.dumps(sd["param_groups"])

    def load_optimizer_tensors(self, opt, tensors, groups):
        import numpy as np

        state = {}
        for key, (shape, data) in tensors.items():
            _, idx, k = key.split(".", 2)
            arr = np.frombuffer(data, dtype="<f4").reshape(shape).copy()
            state.setdefault(int(idx), {})[k] = self.torch.from_numpy(arr)
        opt.load_state_dict({"state": state, "param_groups": json.loads(groups)})


class UnslothBackend(object):
    def __init__(self, api=None):
        if api is None:
            if platform.system() != "Linux":
                raise RuntimeError("NoAccelerator: Unsloth runs on Linux with a CUDA GPU")
            api = RealApi()
        self.api = api

    def versions(self):
        return self.api.versions()

    def prepare(self, cfg, train, valid):
        api = self.api
        self.cfg = cfg
        seq = int(cfg["sequenceLength"])
        model, tokenizer = api.load(cfg["base"]["weights"], seq, bool(cfg.get("quantised", False)))
        n = int(cfg["shapes"]["layers"]) if cfg.get("shapes") else None
        want = int(cfg.get("layers", 16))
        layers = None
        if n is not None and 0 <= want < n:
            layers = list(range(n - want, n))
        self.rank = int(cfg["rank"])
        self.alpha = int(cfg["alpha"])
        self.model = api.peft(model, self.rank, self.alpha, cfg["targets"], int(cfg["seed"]), layers)
        self.tokenizer = tokenizer
        self.pad_id = getattr(tokenizer, "pad_token_id", None) or 0
        self.train = [R.encode(tokenizer, r, cfg["format"], seq) for r in train]
        self.valid = [R.encode(tokenizer, r, cfg["format"], seq) for r in valid]
        self.opt = api.optimizer(self.model, float(cfg["learningRate"]))

    def _labelled(self, batch):
        rows, lengths, prompts = R.pad(batch, self.pad_id)
        labels = []
        for row, n, p in zip(rows, lengths, prompts):
            labels.append([tok if p <= t < n else -100 for t, tok in enumerate(row)])
        tokens = int(sum(max(0, n - p) for n, p in zip(lengths, prompts)))
        return rows, labels, tokens

    def train_step(self, indices, accumulate):
        per = max(1, len(indices) // accumulate)
        batches = []
        tokens = 0
        for i in range(0, len(indices), per):
            ids, labels, n = self._labelled([self.train[j] for j in indices[i : i + per]])
            batches.append((ids, labels))
            tokens += n
        return self.api.step(self.model, self.opt, batches), tokens

    def evaluate(self):
        per = max(1, int(self.cfg["batch"]["perDevice"]))
        total, parts = 0.0, 0
        for i in range(0, len(self.valid), per):
            ids, labels, _ = self._labelled(self.valid[i : i + per])
            total += self.api.eval_loss(self.model, ids, labels)
            parts += 1
        return total / max(1, parts)

    def save(self, d):
        lora = self.api.lora_tensors(self.model)
        P.write_safetensors(
            os.path.join(d, "adapter.safetensors"),
            {k: ("F32", shape, data) for k, (shape, data) in lora.items()},
        )
        opt, groups = self.api.optimizer_tensors(self.opt)
        P.write_safetensors(
            os.path.join(d, "optimizer.safetensors"),
            {k: ("F32", shape, data) for k, (shape, data) in opt.items()},
            {"param_groups": groups},
        )

    def load(self, d):
        lora, _ = P.read_safetensors(os.path.join(d, "adapter.safetensors"))
        self.api.load_lora_tensors(self.model, {k: (shape, data) for k, (_, shape, data) in lora.items()})
        opt, meta = P.read_safetensors(os.path.join(d, "optimizer.safetensors"))
        self.api.load_optimizer_tensors(
            self.opt, {k: (shape, data) for k, (_, shape, data) in opt.items()}, meta.get("param_groups", "[]")
        )

    def export_peft(self, out):
        lora = self.api.lora_tensors(self.model)
        P.write_safetensors(
            os.path.join(out, "adapter_model.safetensors"),
            {k: ("F32", shape, data) for k, (shape, data) in lora.items()},
            {"format": "pt"},
        )
        P.write_json(
            os.path.join(out, "adapter_config.json"),
            P.peft_config(self.cfg["base"]["id"], self.rank, self.alpha, self.cfg["targets"]),
        )
