"""The MLX backend: LoRA and QLoRA on Apple silicon with MLX-LM (MIT).

MLX-LM is used as a library: `mlx_lm.load` reads the local base snapshot (a
4-bit `mlx-community` snapshot makes it QLoRA), `linear_to_lora_layers` adds
the adapters, and the loop, the data order, the loss and the checkpoints are
the driver's own. MLX-LM's trainer saves no optimiser state, so a resumed run
would restart Adam from zero; this loop saves the optimiser state with the
adapter, as safetensors, and a resume continues it.

The exported adapter is in the PEFT layout: MLX keeps `lora_a` as (in, rank)
and `lora_b` as (rank, out) and adds `scale * (x @ lora_a) @ lora_b`; PEFT
keeps A as (rank, in) and B as (out, rank) and adds `(alpha / rank) * B A x`.
So A = lora_a^T, B = lora_b^T and alpha = scale * rank
(test_train_driver.py checks the identity numerically).
"""

import os

import sky_protocol as P
import sky_rows as R


class MlxBackend(object):
    def __init__(self):
        import mlx.core as mx
        import mlx.nn as nn
        import mlx.optimizers as optim
        import mlx_lm

        self.mx, self.nn, self.optim, self.mlx_lm = mx, nn, optim, mlx_lm

    def versions(self):
        import mlx.core as mx

        return {"mlx": getattr(mx, "__version__", "?"), "mlx_lm": getattr(self.mlx_lm, "__version__", "?")}

    def prepare(self, cfg, train, valid):
        mx, nn, optim = self.mx, self.nn, self.optim
        from mlx_lm import load
        from mlx_lm.tuner.utils import linear_to_lora_layers

        if cfg.get("device") == "cpu":
            mx.set_default_device(mx.cpu)
        mx.random.seed(int(cfg["seed"]))
        self.cfg = cfg
        # Base weights are data, never code: no remote code runs.
        model, tokenizer = load(cfg["base"]["weights"], tokenizer_config={"trust_remote_code": False})
        model.freeze()
        n = len(model.layers)
        layers = int(cfg.get("layers", 16))
        if layers < 0 or layers > n:
            layers = n
        self.rank = int(cfg["rank"])
        self.scale = float(cfg["alpha"]) / float(self.rank)
        linear_to_lora_layers(
            model,
            layers,
            {"rank": self.rank, "scale": self.scale, "dropout": 0.0, "keys": list(cfg["targets"])},
        )
        self.model = model
        self.tokenizer = tokenizer
        self.pad_id = tokenizer.pad_token_id if getattr(tokenizer, "pad_token_id", None) is not None else 0
        seq = int(cfg["sequenceLength"])
        self.train = [R.encode(tokenizer, r, cfg["format"], seq) for r in train]
        self.valid = [R.encode(tokenizer, r, cfg["format"], seq) for r in valid]
        self.opt = optim.Adam(learning_rate=float(cfg["learningRate"]))

        def loss_fn(model, inputs, targets, mask):
            logits = model(inputs)
            ce = nn.losses.cross_entropy(logits, targets) * mask
            return ce.sum() / mx.maximum(mask.sum(), 1.0)

        self.loss_fn = loss_fn
        self.loss_and_grad = nn.value_and_grad(model, loss_fn)

    def _arrays(self, batch):
        mx = self.mx
        rows, lengths, prompts = R.pad(batch, self.pad_id)
        width = len(rows[0])
        tokens = mx.array(rows)
        mask = mx.array(R.loss_mask(lengths, prompts, width))
        ntok = int(sum(max(0, n - 1 - max(0, p - 1)) for n, p in zip(lengths, prompts)))
        return tokens[:, :-1], tokens[:, 1:], mask, ntok

    def train_step(self, indices, accumulate):
        mx = self.mx
        from mlx.utils import tree_map

        per = max(1, len(indices) // accumulate)
        grads = None
        total_loss = 0.0
        tokens = 0
        parts = 0
        for i in range(0, len(indices), per):
            chunk = [self.train[j] for j in indices[i : i + per]]
            inputs, targets, mask, ntok = self._arrays(chunk)
            loss, g = self.loss_and_grad(self.model, inputs, targets, mask)
            grads = g if grads is None else tree_map(lambda a, b: a + b, grads, g)
            total_loss += loss.item()
            tokens += ntok
            parts += 1
        if parts > 1:
            grads = tree_map(lambda a: a / parts, grads)
        self.opt.update(self.model, grads)
        mx.eval(self.model.parameters(), self.opt.state)
        return total_loss / parts, tokens

    def evaluate(self):
        per = max(1, int(self.cfg["batch"]["perDevice"]))
        total = 0.0
        parts = 0
        for i in range(0, len(self.valid), per):
            inputs, targets, mask, _ = self._arrays(self.valid[i : i + per])
            total += self.loss_fn(self.model, inputs, targets, mask).item()
            parts += 1
        return total / max(1, parts)

    def save(self, d):
        from mlx.utils import tree_flatten

        mx = self.mx
        mx.save_safetensors(os.path.join(d, "adapter.safetensors"), dict(tree_flatten(self.model.trainable_parameters())))
        mx.save_safetensors(os.path.join(d, "optimizer.safetensors"), dict(tree_flatten(self.opt.state)))

    def load(self, d):
        from mlx.utils import tree_unflatten

        mx = self.mx
        weights = mx.load(os.path.join(d, "adapter.safetensors"))
        self.model.load_weights(list(weights.items()), strict=False)
        self.opt.state = tree_unflatten(list(mx.load(os.path.join(d, "optimizer.safetensors")).items()))
        mx.eval(self.model.parameters(), self.opt.state)

    def export_peft(self, out):
        import numpy as np
        from mlx.utils import tree_flatten

        mx = self.mx
        tensors = {}
        for key, value in tree_flatten(self.model.trainable_parameters()):
            name = P.mlx_to_peft_name(key)
            if name is None:
                continue
            arr = np.ascontiguousarray(np.array(value.astype(mx.float32)).T, dtype="<f4")
            tensors[name] = ("F32", arr.shape, arr.tobytes())
        P.write_safetensors(os.path.join(out, "adapter_model.safetensors"), tensors, {"format": "pt"})
        P.write_json(
            os.path.join(out, "adapter_config.json"),
            P.peft_config(self.cfg["base"]["id"], self.rank, self.scale * self.rank, self.cfg["targets"]),
        )
