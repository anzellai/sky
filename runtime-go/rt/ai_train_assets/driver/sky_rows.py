"""Rows to tokens, shared by the MLX and Unsloth backends.

A row is a Std.Ai.Dataset row as the stage step wrote it: a chat row
`{"messages": [{"role", "content"}, ...]}` or an instruction row
`{"prompt", "completion"}`. Both become one conversation rendered with the
base model's own chat template. The loss counts the last assistant turn only:
the tokens before it are the prompt.
"""


def messages(row, fmt):
    if fmt == "instruction":
        return [
            {"role": "user", "content": row["prompt"]},
            {"role": "assistant", "content": row["completion"]},
        ]
    return [{"role": m["role"], "content": m["content"]} for m in row["messages"]]


def _ids(value):
    # transformers returns a list of ints, or a BatchEncoding with input_ids.
    if isinstance(value, dict) or hasattr(value, "keys"):
        value = value["input_ids"]
    return [int(t) for t in value]


def encode(tokenizer, row, fmt, seq_len):
    """(token ids, prompt length) for one row, cut to seq_len tokens."""
    msgs = messages(row, fmt)
    full = _ids(tokenizer.apply_chat_template(msgs, tokenize=True))
    prompt = 0
    if msgs and msgs[-1]["role"] == "assistant":
        prompt = len(_ids(tokenizer.apply_chat_template(msgs[:-1], tokenize=True, add_generation_prompt=True)))
    full = full[:seq_len]
    return full, min(prompt, max(0, len(full) - 1))


def pad(batch, pad_id=0):
    """Pad token lists to one length: (rows, lengths, prompt lengths)."""
    width = max(len(ids) for ids, _ in batch)
    rows = [ids + [pad_id] * (width - len(ids)) for ids, _ in batch]
    return rows, [len(ids) for ids, _ in batch], [p for _, p in batch]


def loss_mask(lengths, prompts, width):
    """1.0 where a target token counts: after the prompt, before the padding.

    Position t predicts token t + 1, so the mask covers t in [prompt - 1,
    length - 1) for each row, with a floor of 0.
    """
    out = []
    for n, p in zip(lengths, prompts):
        lo = max(0, p - 1)
        out.append([1.0 if lo <= t < n - 1 else 0.0 for t in range(width - 1)])
    return out
