/* sky_trainer.c - the native LoRA trainer of Sky's Std.Ai.Train.
 *
 * C11 on MLX through mlx-c. No Python and no network. The Go runtime does
 * all parsing (the base's config.json, the tokenizer and chat format, the
 * data order) and starts this program with flags; it reads only fixed binary
 * files and safetensors:
 *
 *   <run>/data/train.tok, valid.tok   token ids and prompt lengths per row
 *   <run>/data/order.bin              the row indices of every optimiser step
 *   <model>/model.safetensors         the base weights (Hugging Face names);
 *                                     dense, or MLX-quantised (.scales/.biases)
 *
 * It writes protocol v1 lines to standard output (the Go side redirects them
 * to <run>/events.jsonl): hello, progress, metric, checkpoint, artefact,
 * cancelled, done, error. Checkpoints are written to a temporary directory,
 * flushed, given a SHA-256 manifest last, and renamed into place before their
 * line is written: checkpoints/<7 digits>/{adapter,optimizer}.safetensors,
 * state.json, manifest.json. The finished adapter is PEFT-shaped
 * (<run>/adapter/adapter_model.safetensors + adapter_config.json, A is
 * rank x in and B is out x rank, F32), which Go converts to a GGUF LoRA.
 *
 * Before any MLX state exists it forks a guard (guard.c) that kills it above
 * --memory-cap bytes of phys_footprint and reports ModelTooLarge.
 *
 *   sky-trainer --selftest [--cpu]
 *   sky-trainer --run DIR --model DIR [shape flags] [training flags]
 *
 * Run `sky-trainer --help` for the flags.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <signal.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "guard.h"
#include "mlx/c/mlx.h"
#include "sha256.h"

#define SKY_TRAINER_VERSION "0.1.0"
#ifndef SKY_MLX_VERSION
#error "build with -DSKY_MLX_VERSION=\"x.y.z\" (scripts/ai/trainer/build.sh does)"
#endif

/* ─── failure and protocol lines ─────────────────────────────────────── */

static char g_mlx_err[2048];
static pid_t g_guard = 0;

static void on_mlx_error(const char* msg, void* data) {
  (void)data;
  snprintf(g_mlx_err, sizeof g_mlx_err, "%s", msg ? msg : "unknown MLX error");
}

/* A JSON string literal of s, escaped. */
static void json_str(char* out, size_t cap, const char* s) {
  size_t o = 0;
  if (cap < 3) return;
  out[o++] = '"';
  for (; *s && o + 7 < cap; s++) {
    unsigned char c = (unsigned char)*s;
    if (c == '"' || c == '\\') {
      out[o++] = '\\';
      out[o++] = (char)c;
    } else if (c < 0x20) {
      o += (size_t)snprintf(out + o, cap - o, "\\u%04x", c);
    } else {
      out[o++] = (char)c;
    }
  }
  out[o++] = '"';
  out[o] = 0;
}

/* A JSON number, or null when it is not finite (the Python driver's rule). */
static void json_num(char* out, size_t cap, double v) {
  if (!isfinite(v))
    snprintf(out, cap, "null");
  else
    snprintf(out, cap, "%.9g", v);
}

static void emit(const char* line) {
  fputs(line, stdout);
  fputc('\n', stdout);
  fflush(stdout);
}

static void fail(const char* code, const char* fmt, ...) {
  char msg[2048], qmsg[4400], qcode[128], line[4800];
  va_list ap;
  va_start(ap, fmt);
  vsnprintf(msg, sizeof msg, fmt, ap);
  va_end(ap);
  json_str(qmsg, sizeof qmsg, msg);
  json_str(qcode, sizeof qcode, code);
  snprintf(line, sizeof line, "{\"code\":%s,\"message\":%s,\"type\":\"error\",\"v\":1}", qcode, qmsg);
  emit(line);
  fprintf(stderr, "sky-trainer: %s: %s\n", code, msg);
  sky_guard_stop(g_guard);
  exit(1);
}

static void ck_fail(const char* file, int line) {
  const char* code = "TrainingFailed";
  if (strstr(g_mlx_err, "memory") || strstr(g_mlx_err, "allocat")) code = "ModelTooLarge";
  fail(code, "MLX failed at %s:%d: %s", file, line, g_mlx_err[0] ? g_mlx_err : "no message");
}
#define CK(x)                                    \
  do {                                           \
    if ((x) != 0) ck_fail(__FILE__, __LINE__);   \
  } while (0)

/* ─── configuration ──────────────────────────────────────────────────── */

enum { T_Q, T_K, T_V, T_O, T_GATE, T_UP, T_DOWN, NTARGET };
static const char* TNAME[NTARGET] = {"q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"};
static const char* TMOD[NTARGET] = {"self_attn", "self_attn", "self_attn", "self_attn", "mlp", "mlp", "mlp"};

typedef struct {
  const char *run, *model, *base_id;
  const char* weights[64];
  int nweights;
  int layers, hidden, heads, kv_heads, inter, vocab, tied, head_dim;
  float rope_theta, rms_eps;
  int rank;
  float alpha;
  int lora_layers;
  unsigned targets;
  float lr, wd;
  int batch, accumulate, ckpt_every, eval_every, max_eval_rows;
  uint64_t seed;
  int quant_bits, quant_group; /* quantise a dense base on load */
  int base_bits, base_group;   /* an already-quantised base */
  int grad_ckpt, use_cpu;
  int loss_chunk; /* tokens per loss chunk; 0 unchunked, -1 automatic */
  uint64_t cap, cache_limit;
  int resume_step;
  const char* resume_dir;
  int attempt, crash_at, pace_ms;
} Cfg;

static Cfg C;
static mlx_stream S;

/* ─── temporaries: every intermediate handle is freed after a step ───── */

static mlx_array* pool = NULL;
static size_t npool = 0, cappool = 0;
static mlx_array* TP(void) {
  if (npool == cappool) {
    cappool = cappool ? cappool * 2 : 4096;
    pool = realloc(pool, cappool * sizeof *pool);
    if (!pool) fail("ModelTooLarge", "out of host memory for array handles");
  }
  pool[npool] = mlx_array_new();
  return &pool[npool++];
}
static void pool_free(void) {
  for (size_t i = 0; i < npool; i++) mlx_array_free(pool[i]);
  npool = 0;
}
static mlx_array mm(mlx_array a, mlx_array b) { mlx_array* r = TP(); CK(mlx_matmul(r, a, b, S)); return *r; }
static mlx_array add(mlx_array a, mlx_array b) { mlx_array* r = TP(); CK(mlx_add(r, a, b, S)); return *r; }
static mlx_array sub(mlx_array a, mlx_array b) { mlx_array* r = TP(); CK(mlx_subtract(r, a, b, S)); return *r; }
static mlx_array mul(mlx_array a, mlx_array b) { mlx_array* r = TP(); CK(mlx_multiply(r, a, b, S)); return *r; }
static mlx_array dv(mlx_array a, mlx_array b) { mlx_array* r = TP(); CK(mlx_divide(r, a, b, S)); return *r; }
static mlx_array sc(float v) { mlx_array* r = TP(); mlx_array_free(*r); *r = mlx_array_new_float(v); return *r; }
static mlx_array tr(mlx_array a) { mlx_array* r = TP(); CK(mlx_swapaxes(r, a, -1, -2, S)); return *r; }
static mlx_array rs(mlx_array a, const int* sh, int n) { mlx_array* r = TP(); CK(mlx_reshape(r, a, sh, (size_t)n, S)); return *r; }
static mlx_array sw(mlx_array a, int x, int y) { mlx_array* r = TP(); CK(mlx_swapaxes(r, a, x, y, S)); return *r; }
static mlx_array ast(mlx_array a, mlx_dtype t) { mlx_array* r = TP(); CK(mlx_astype(r, a, t, S)); return *r; }
static mlx_array sq(mlx_array a) { mlx_array* r = TP(); CK(mlx_square(r, a, S)); return *r; }
static mlx_array sqr(mlx_array a) { mlx_array* r = TP(); CK(mlx_sqrt(r, a, S)); return *r; }
static mlx_array sigm(mlx_array a) { mlx_array* r = TP(); CK(mlx_sigmoid(r, a, S)); return *r; }
static mlx_array rms(mlx_array x, mlx_array w) { mlx_array* r = TP(); CK(mlx_fast_rms_norm(r, x, w, C.rms_eps, S)); return *r; }
static mlx_array rope(mlx_array x) {
  mlx_array* r = TP();
  mlx_optional_float base = {C.rope_theta, true};
  mlx_array nul = {0};
  CK(mlx_fast_rope(r, x, C.head_dim, false, base, 1.0f, 0, nul, S));
  return *r;
}

/* ─── weights ────────────────────────────────────────────────────────── */

typedef struct {
  mlx_array w, b;       /* dense; b may be null */
  mlx_array qw, qs, qb; /* quantised */
  int quant, bits, group;
} Lin;
typedef struct {
  Lin p[NTARGET];
  mlx_array ln1, ln2;
} Layer;

static Layer* L;
static Lin EMB, HEAD, *OUT; /* OUT is &EMB when tied */
static mlx_array NORM;
static mlx_map_string_to_array WM;

static int target_in(int t) {
  if (t == T_O) return C.heads * C.head_dim;
  if (t == T_DOWN) return C.inter;
  return C.hidden;
}
static int target_out(int t) {
  switch (t) {
    case T_Q: return C.heads * C.head_dim;
    case T_K:
    case T_V: return C.kv_heads * C.head_dim;
    case T_GATE:
    case T_UP: return C.inter;
    default: return C.hidden;
  }
}

static int wfind(const char* name, mlx_array* out) {
  *out = mlx_array_new();
  if (mlx_map_string_to_array_get(out, WM, name) != 0) {
    mlx_array_free(*out);
    *out = (mlx_array){0};
    g_mlx_err[0] = 0;
    return 0;
  }
  return 1;
}
static mlx_array wget(const char* name) {
  mlx_array a;
  if (!wfind(name, &a)) fail("InvalidSetting", "the base has no tensor %s", name);
  return a;
}

static void eval_arrays(const mlx_array* xs, int n) {
  mlx_vector_array v = mlx_vector_array_new();
  for (int i = 0; i < n; i++)
    if (xs[i].ctx) mlx_vector_array_append_value(v, xs[i]);
  CK(mlx_eval(v));
  mlx_vector_array_free(v);
}

/* Loads <prefix>.weight (+ .bias) as a dense or quantised linear. */
static void lin_load(Lin* l, const char* prefix, int bias) {
  char k[512];
  memset(l, 0, sizeof *l);
  snprintf(k, sizeof k, "%s.scales", prefix);
  mlx_array scales;
  if (wfind(k, &scales)) {
    l->quant = 1;
    l->bits = C.base_bits;
    l->group = C.base_group;
    l->qs = scales;
    snprintf(k, sizeof k, "%s.weight", prefix);
    l->qw = wget(k);
    snprintf(k, sizeof k, "%s.biases", prefix);
    l->qb = wget(k);
  } else {
    snprintf(k, sizeof k, "%s.weight", prefix);
    l->w = wget(k);
    if (C.quant_bits > 0) {
      mlx_vector_array out = mlx_vector_array_new();
      mlx_optional_int gs = {C.quant_group, true}, bits = {C.quant_bits, true};
      mlx_array nul = {0};
      CK(mlx_quantize(&out, l->w, gs, bits, "affine", nul, S));
      l->qw = mlx_array_new();
      l->qs = mlx_array_new();
      l->qb = mlx_array_new();
      mlx_vector_array_get(&l->qw, out, 0);
      mlx_vector_array_get(&l->qs, out, 1);
      mlx_vector_array_get(&l->qb, out, 2);
      mlx_vector_array_free(out);
      mlx_array qs[3] = {l->qw, l->qs, l->qb};
      eval_arrays(qs, 3);
      mlx_array_free(l->w);
      l->w = (mlx_array){0};
      l->quant = 1;
      l->bits = C.quant_bits;
      l->group = C.quant_group;
    }
  }
  if (bias) {
    snprintf(k, sizeof k, "%s.bias", prefix);
    l->b = wget(k);
  }
}

static mlx_array linear(const Lin* l, mlx_array x) {
  mlx_array y;
  if (l->quant) {
    mlx_array* r = TP();
    mlx_optional_int gs = {l->group, true}, bits = {l->bits, true};
    CK(mlx_quantized_matmul(r, x, l->qw, l->qs, l->qb, true, gs, bits, "affine", S));
    y = *r;
  } else {
    y = mm(x, tr(l->w));
  }
  if (l->b.ctx) y = add(y, l->b);
  return y;
}

/* Rows of the embedding for ids; a quantised table is gathered, then only
 * the gathered rows are dequantised. */
static mlx_array embed(mlx_array ids) {
  if (!EMB.quant) {
    mlx_array* r = TP();
    CK(mlx_take_axis(r, EMB.w, ids, 0, S));
    return *r;
  }
  mlx_array *w = TP(), *s = TP(), *b = TP(), *r = TP();
  CK(mlx_take_axis(w, EMB.qw, ids, 0, S));
  CK(mlx_take_axis(s, EMB.qs, ids, 0, S));
  CK(mlx_take_axis(b, EMB.qb, ids, 0, S));
  mlx_optional_int gs = {EMB.group, true}, bits = {EMB.bits, true};
  mlx_optional_dtype dt = {MLX_FLOAT32, false};
  mlx_array nul = {0};
  CK(mlx_dequantize(r, *w, *s, *b, gs, bits, "affine", nul, dt, S));
  return *r;
}

static void load_model(void) {
  WM = mlx_map_string_to_array_new();
  mlx_stream cpu = mlx_default_cpu_stream_new();
  if (C.nweights == 0) {
    static char def[4096];
    snprintf(def, sizeof def, "%s/model.safetensors", C.model);
    C.weights[C.nweights++] = def;
  }
  for (int i = 0; i < C.nweights; i++) {
    mlx_map_string_to_array part = mlx_map_string_to_array_new();
    mlx_map_string_to_string meta = mlx_map_string_to_string_new();
    if (mlx_load_safetensors(&part, &meta, C.weights[i], cpu) != 0)
      fail("InvalidSetting", "cannot read the base weights %s: %s", C.weights[i], g_mlx_err);
    mlx_map_string_to_array_iterator it = mlx_map_string_to_array_iterator_new(part);
    const char* key;
    mlx_array val = mlx_array_new();
    while (mlx_map_string_to_array_iterator_next(&key, &val, it) == 0) mlx_map_string_to_array_insert(WM, key, val);
    mlx_array_free(val);
    mlx_map_string_to_array_iterator_free(it);
    mlx_map_string_to_array_free(part);
    mlx_map_string_to_string_free(meta);
  }
  mlx_stream_free(cpu);
  L = calloc((size_t)C.layers, sizeof *L);
  lin_load(&EMB, "model.embed_tokens", 0);
  NORM = wget("model.norm.weight");
  if (C.tied) {
    OUT = &EMB;
  } else {
    lin_load(&HEAD, "lm_head", 0);
    OUT = &HEAD;
  }
  for (int i = 0; i < C.layers; i++) {
    char p[256];
    for (int t = 0; t < NTARGET; t++) {
      snprintf(p, sizeof p, "model.layers.%d.%s.%s", i, TMOD[t], TNAME[t]);
      lin_load(&L[i].p[t], p, t == T_Q || t == T_K || t == T_V);
    }
    snprintf(p, sizeof p, "model.layers.%d.input_layernorm.weight", i);
    L[i].ln1 = wget(p);
    snprintf(p, sizeof p, "model.layers.%d.post_attention_layernorm.weight", i);
    L[i].ln2 = wget(p);
  }
  /* The handles above own their arrays; the name map is no longer needed. */
  mlx_map_string_to_array_free(WM);
  WM = mlx_map_string_to_array_new();
  int vocab_rows = mlx_array_shape(EMB.quant ? EMB.qw : EMB.w)[0];
  if (vocab_rows != C.vocab)
    fail("InvalidSetting", "the base's embedding has %d rows and --vocab says %d", vocab_rows, C.vocab);
}

/* ─── LoRA parameters ────────────────────────────────────────────────── */

static int first_lora_layer(void) {
  int k = C.lora_layers <= 0 || C.lora_layers > C.layers ? C.layers : C.lora_layers;
  return C.layers - k;
}
static int ntargets(void) {
  int n = 0;
  for (int t = 0; t < NTARGET; t++)
    if (C.targets & (1u << t)) n++;
  return n;
}
static int NP;              /* number of parameter arrays */
static mlx_array *P, *M1, *M2;
static char (*PNAME)[160]; /* the PEFT name of each parameter */

/* Index of the A matrix of target t in layer li, or -1. B is the next one. */
static int param_index(int li, int t) {
  int f = first_lora_layer();
  if (li < f || !(C.targets & (1u << t))) return -1;
  int k = 0;
  for (int u = 0; u < t; u++)
    if (C.targets & (1u << u)) k++;
  return ((li - f) * ntargets() + k) * 2;
}

static void params_alloc(void) {
  NP = (C.layers - first_lora_layer()) * ntargets() * 2;
  P = calloc((size_t)NP, sizeof *P);
  M1 = calloc((size_t)NP, sizeof *M1);
  M2 = calloc((size_t)NP, sizeof *M2);
  PNAME = calloc((size_t)NP, sizeof *PNAME);
  for (int li = first_lora_layer(); li < C.layers; li++)
    for (int t = 0; t < NTARGET; t++) {
      int i = param_index(li, t);
      if (i < 0) continue;
      snprintf(PNAME[i], sizeof PNAME[i], "base_model.model.model.layers.%d.%s.%s.lora_A.weight", li, TMOD[t], TNAME[t]);
      snprintf(PNAME[i + 1], sizeof PNAME[i + 1], "base_model.model.model.layers.%d.%s.%s.lora_B.weight", li, TMOD[t],
               TNAME[t]);
    }
}

/* A ~ U(-1/sqrt(in), 1/sqrt(in)) from the seed, B = 0, moments 0. */
static void params_init(uint64_t seed) {
  mlx_array key = mlx_array_new();
  CK(mlx_random_key(&key, seed));
  for (int li = first_lora_layer(); li < C.layers; li++)
    for (int t = 0; t < NTARGET; t++) {
      int i = param_index(li, t);
      if (i < 0) continue;
      int in = target_in(t), out = target_out(t);
      int sa[2] = {C.rank, in}, sb[2] = {out, C.rank};
      float bnd = 1.0f / sqrtf((float)in);
      mlx_array lo = mlx_array_new_float(-bnd), hi = mlx_array_new_float(bnd);
      mlx_array k1 = mlx_array_new(), k2 = mlx_array_new();
      CK(mlx_random_split(&k1, &k2, key, S));
      mlx_array_set(&key, k1);
      P[i] = mlx_array_new();
      P[i + 1] = mlx_array_new();
      CK(mlx_random_uniform(&P[i], lo, hi, sa, 2, MLX_FLOAT32, k2, S));
      CK(mlx_zeros(&P[i + 1], sb, 2, MLX_FLOAT32, S));
      mlx_array_free(lo);
      mlx_array_free(hi);
      mlx_array_free(k1);
      mlx_array_free(k2);
    }
  mlx_array_free(key);
  for (int i = 0; i < NP; i++) {
    M1[i] = mlx_array_new();
    M2[i] = mlx_array_new();
    CK(mlx_zeros_like(&M1[i], P[i], S));
    CK(mlx_zeros_like(&M2[i], P[i], S));
  }
  eval_arrays(P, NP);
  eval_arrays(M1, NP);
  eval_arrays(M2, NP);
}

/* ─── the model ──────────────────────────────────────────────────────── */

static mlx_array proj(int li, int t, const mlx_array* prm, mlx_array h) {
  mlx_array y = linear(&L[li].p[t], h);
  int i = param_index(li, t);
  if (prm && i >= 0) {
    mlx_array z = mm(mm(h, tr(prm[i])), tr(prm[i + 1]));
    z = mul(z, sc(C.alpha / (float)C.rank));
    y = add(y, ast(z, mlx_array_dtype(y)));
  }
  return y;
}

static mlx_array layer_fwd(int li, mlx_array x, const mlx_array* prm) {
  const int* xs = mlx_array_shape(x);
  int bsz = xs[0], len = xs[1];
  int sh4q[4] = {bsz, len, C.heads, C.head_dim}, sh4k[4] = {bsz, len, C.kv_heads, C.head_dim};
  int sh3[3] = {bsz, len, C.heads * C.head_dim};
  mlx_array h = rms(x, L[li].ln1);
  mlx_array q = proj(li, T_Q, prm, h), k = proj(li, T_K, prm, h), v = proj(li, T_V, prm, h);
  q = rope(sw(rs(q, sh4q, 4), 1, 2));
  k = rope(sw(rs(k, sh4k, 4), 1, 2));
  v = sw(rs(v, sh4k, 4), 1, 2);
  mlx_array* ar = TP();
  mlx_array nul = {0};
  CK(mlx_fast_scaled_dot_product_attention(ar, q, k, v, 1.0f / sqrtf((float)C.head_dim), "causal", nul, nul, false, S));
  mlx_array a = rs(sw(*ar, 1, 2), sh3, 3);
  x = add(x, proj(li, T_O, prm, a));
  mlx_array h2 = rms(x, L[li].ln2);
  mlx_array g = proj(li, T_GATE, prm, h2);
  mlx_array act = mul(mul(g, sigm(g)), proj(li, T_UP, prm, h2));
  return add(x, proj(li, T_DOWN, prm, act));
}

/* Gradient checkpointing of the unchunked loss (--loss-chunk 0, a test path):
 * each LoRA layer is an MLX closure wrapped by mlx_checkpoint, so its
 * activations are recomputed in the backward pass instead of kept. The
 * chunked loss, which Std.Ai.Train runs, recomputes each layer anyway, so Sky
 * never passes --grad-checkpoint. Inputs: [x, the layer's parameters in param
 * order]. */
static mlx_closure* CKL = NULL;
static int layer_closure(mlx_vector_array* res, const mlx_vector_array in, void* payload) {
  int li = (int)(intptr_t)payload;
  mlx_array* prm = calloc((size_t)NP, sizeof *prm);
  mlx_array x = mlx_array_new();
  mlx_vector_array_get(&x, in, 0);
  int at = 1;
  for (int t = 0; t < NTARGET; t++) {
    int i = param_index(li, t);
    if (i < 0) continue;
    prm[i] = mlx_array_new();
    prm[i + 1] = mlx_array_new();
    mlx_vector_array_get(&prm[i], in, (size_t)at++);
    mlx_vector_array_get(&prm[i + 1], in, (size_t)at++);
  }
  mlx_array y = layer_fwd(li, x, prm);
  mlx_vector_array_set_value(res, y);
  for (int i = 0; i < NP; i++)
    if (prm[i].ctx) mlx_array_free(prm[i]);
  free(prm);
  mlx_array_free(x);
  return 0;
}
static void checkpoint_closures(void) {
  CKL = calloc((size_t)C.layers, sizeof *CKL);
  for (int li = first_lora_layer(); li < C.layers; li++) {
    mlx_closure f = mlx_closure_new_func_payload(layer_closure, (void*)(intptr_t)li, NULL);
    CKL[li] = mlx_closure_new();
    CK(mlx_checkpoint(&CKL[li], f));
    mlx_closure_free(f);
  }
}

/* The hidden states after the final norm, [B, len, hidden]; prm NULL runs
 * the base alone. */
static mlx_array hidden_states(const mlx_array* prm, mlx_array ids) {
  mlx_array x = embed(ids);
  for (int li = 0; li < C.layers; li++) {
    if (prm && C.grad_ckpt && CKL && li >= first_lora_layer()) {
      mlx_vector_array in = mlx_vector_array_new(), out = mlx_vector_array_new();
      mlx_vector_array_append_value(in, x);
      for (int t = 0; t < NTARGET; t++) {
        int i = param_index(li, t);
        if (i < 0) continue;
        mlx_vector_array_append_value(in, prm[i]);
        mlx_vector_array_append_value(in, prm[i + 1]);
      }
      CK(mlx_closure_apply(&out, CKL[li], in));
      mlx_array* y = TP();
      mlx_vector_array_get(y, out, 0);
      x = *y;
      mlx_vector_array_free(in);
      mlx_vector_array_free(out);
    } else {
      x = layer_fwd(li, x, prm);
    }
  }
  return rms(x, NORM);
}

/* The summed masked NLL of some tokens: logits [..., vocab] float32, targets
 * and mask of the same leading shape. */
static mlx_array masked_nll_sum(mlx_array logits, mlx_array tg, mlx_array mk) {
  mlx_array *lse = TP(), *te = TP(), *tl = TP(), *tl2 = TP(), *s = TP();
  CK(mlx_logsumexp_axis(lse, logits, -1, false, S));
  CK(mlx_expand_dims(te, tg, -1, S));
  CK(mlx_take_along_axis(tl, logits, *te, -1, S));
  CK(mlx_squeeze_axis(tl2, *tl, -1, S));
  CK(mlx_sum(s, mul(sub(*lse, *tl2), mk), false, S));
  return *s;
}

/* ─── the loss ───────────────────────────────────────────────────────── */

/* The unchunked loss closure: [P..., ids, targets, mask] -> [mean masked
 * NLL], over the whole [tokens x vocab] logits. --loss-chunk 0 trains with it,
 * and the selftest holds the chunked loss to it. */
static int loss_closure(mlx_vector_array* res, const mlx_vector_array in, void* payload) {
  int use_params = payload != NULL;
  mlx_array* prm = calloc((size_t)NP + 1, sizeof *prm);
  int base = use_params ? NP : 0;
  for (int i = 0; i < base; i++) {
    prm[i] = mlx_array_new();
    mlx_vector_array_get(&prm[i], in, (size_t)i);
  }
  mlx_array ids = mlx_array_new(), tg = mlx_array_new(), mk = mlx_array_new();
  mlx_vector_array_get(&ids, in, (size_t)base);
  mlx_vector_array_get(&tg, in, (size_t)base + 1);
  mlx_vector_array_get(&mk, in, (size_t)base + 2);
  mlx_array h = hidden_states(use_params ? prm : NULL, ids);
  mlx_array nll = masked_nll_sum(ast(linear(OUT, h), MLX_FLOAT32), tg, mk);
  mlx_array *s2 = TP(), *s2c = TP();
  CK(mlx_sum(s2, mk, false, S));
  CK(mlx_maximum(s2c, *s2, sc(1.0f), S));
  mlx_vector_array_set_value(res, dv(nll, *s2c));
  for (int i = 0; i < base; i++) mlx_array_free(prm[i]);
  free(prm);
  mlx_array_free(ids);
  mlx_array_free(tg);
  mlx_array_free(mk);
  return 0;
}

/* The chunked loss, backpropagated one block at a time. The lm_head and the
 * cross-entropy run over chunks of tokens, and every block of the backward
 * pass is evaluated before the next one starts, so the [tokens x vocab]
 * logits never exist at once and only one LoRA layer's activations are live.
 * (Chunks or layers wrapped in mlx_checkpoint inside one lazy graph do not do
 * this: MLX keeps every recomputed chunk's logits and every layer's
 * activations until the whole graph is evaluated, measured on the dev base
 * at a higher peak than the unchunked loss.) The steps:
 *
 *   1. the frozen layers below the first LoRA layer, each evaluated;
 *   2. each LoRA layer forward, evaluated; its input is kept ([tokens x
 *      hidden] a layer);
 *   3. each chunk of tokens: the final norm, the lm_head and the summed masked
 *      NLL, with its gradient on the chunk's rows of the last layer's output,
 *      evaluated;
 *   4. each LoRA layer from the top: one vjp with the gradient from above,
 *      giving the gradient for the layer below and the layer's LoRA
 *      gradients, evaluated.
 *
 * Step 4 recomputes each LoRA layer's forward, as gradient checkpointing does,
 * so --grad-checkpoint changes only the unchunked loss. A chunk's targets and
 * mask are its closure's payload, not its inputs: the gradient goes to the
 * hidden states only. */
typedef struct {
  mlx_array tg, mk;
} ChunkData;
static void chunk_free(void* p) {
  ChunkData* d = p;
  mlx_array_free(d->tg);
  mlx_array_free(d->mk);
  free(d);
}
/* [x rows before the final norm] -> [summed masked NLL of those rows] */
static int chunk_closure(mlx_vector_array* res, const mlx_vector_array in, void* payload) {
  const ChunkData* d = payload;
  mlx_array x = mlx_array_new();
  mlx_vector_array_get(&x, in, 0);
  mlx_array logits = ast(linear(OUT, rms(x, NORM)), MLX_FLOAT32);
  mlx_vector_array_set_value(res, masked_nll_sum(logits, d->tg, d->mk));
  mlx_array_free(x);
  return 0;
}

/* Tokens per loss chunk: --loss-chunk, else about 8M logits (32 MB of
 * float32) a chunk. 0 is the unchunked loss. */
static int loss_chunk_tokens(void) {
  if (C.loss_chunk >= 0) return C.loss_chunk;
  int k = (1 << 23) / (C.vocab > 0 ? C.vocab : 1);
  return k < 1 ? 1 : k;
}

static mlx_array keep(mlx_array a) {
  mlx_array k = mlx_array_new();
  mlx_array_set(&k, a);
  return k;
}
static float scalar(mlx_array a) {
  CK(mlx_array_eval(a));
  float v = 0;
  CK(mlx_array_item_float32(&v, a));
  return v;
}
static void eval2(mlx_array a, mlx_array b) {
  mlx_vector_array ev = mlx_vector_array_new();
  mlx_vector_array_append_value(ev, a);
  mlx_vector_array_append_value(ev, b);
  CK(mlx_eval(ev));
  mlx_vector_array_free(ev);
}

/* The mean masked NLL of a micro-batch over chunks of k tokens. prm NULL runs
 * the base alone. With G (NP handles), G receives the gradient over prm,
 * evaluated. Frees every temporary it made. */
static float chunked_loss(const mlx_array* prm, mlx_array ids, mlx_array tg, mlx_array mk, int k, mlx_array* G) {
  int first = G ? first_lora_layer() : C.layers;
  int nl = C.layers - first;
  mlx_array* xs = calloc((size_t)nl + 1, sizeof *xs); /* xs[j]: the input of layer first + j */
  /* The layers below, one evaluated at a time: a lazy stack of them keeps
     every layer's activations live until the stack is evaluated. */
  xs[0] = keep(embed(ids));
  CK(mlx_array_eval(xs[0]));
  pool_free();
  for (int li = 0; li < first; li++) {
    mlx_array y = keep(layer_fwd(li, xs[0], prm));
    CK(mlx_array_eval(y));
    mlx_array_free(xs[0]);
    xs[0] = y;
    pool_free();
  }
  for (int j = 0; j < nl; j++) {
    xs[j + 1] = keep(layer_fwd(first + j, xs[j], prm));
    CK(mlx_array_eval(xs[j + 1]));
    pool_free();
  }
  mlx_array top = xs[nl];
  mlx_array* ms = TP();
  CK(mlx_sum(ms, mk, false, S));
  float den = scalar(*ms);
  if (den < 1.0f) den = 1.0f;
  const int* hs = mlx_array_shape(top);
  int bsz = hs[0], len = hs[1], n = bsz * len;
  int sh2[2] = {n, C.hidden}, sh1[1] = {n}, sh3[3] = {bsz, len, C.hidden};
  mlx_array hf = rs(top, sh2, 2), tf = rs(tg, sh1, 1), mf = rs(mk, sh1, 1);
  int nchunks = (n + k - 1) / k;
  mlx_vector_array hv = mlx_vector_array_new(), tv = mlx_vector_array_new(), mv = mlx_vector_array_new();
  if (nchunks > 1) {
    int* at = malloc((size_t)(nchunks - 1) * sizeof *at);
    for (int c = 1; c < nchunks; c++) at[c - 1] = c * k;
    CK(mlx_split_sections(&hv, hf, at, (size_t)(nchunks - 1), 0, S));
    CK(mlx_split_sections(&tv, tf, at, (size_t)(nchunks - 1), 0, S));
    CK(mlx_split_sections(&mv, mf, at, (size_t)(nchunks - 1), 0, S));
    free(at);
  } else {
    mlx_vector_array_append_value(hv, hf);
    mlx_vector_array_append_value(tv, tf);
    mlx_vector_array_append_value(mv, mf);
  }
  pool_free();
  double sum = 0;
  mlx_vector_array dhv = mlx_vector_array_new();
  int arg0 = 0;
  for (int c = 0; c < nchunks; c++) {
    ChunkData* d = malloc(sizeof *d);
    d->tg = mlx_array_new();
    d->mk = mlx_array_new();
    mlx_vector_array_get(&d->tg, tv, (size_t)c);
    mlx_vector_array_get(&d->mk, mv, (size_t)c);
    mlx_closure f = mlx_closure_new_func_payload(chunk_closure, d, chunk_free);
    mlx_array hc = mlx_array_new();
    mlx_vector_array_get(&hc, hv, (size_t)c);
    mlx_vector_array in = mlx_vector_array_new_value(hc), vals = mlx_vector_array_new();
    mlx_array l = mlx_array_new();
    if (G) {
      mlx_closure_value_and_grad vg = mlx_closure_value_and_grad_new();
      CK(mlx_value_and_grad(&vg, f, &arg0, 1));
      mlx_vector_array grads = mlx_vector_array_new();
      CK(mlx_closure_value_and_grad_apply(&vals, &grads, vg, in));
      mlx_array g = mlx_array_new();
      mlx_vector_array_get(&l, vals, 0);
      mlx_vector_array_get(&g, grads, 0);
      eval2(l, g);
      mlx_vector_array_append_value(dhv, g);
      mlx_array_free(g);
      mlx_vector_array_free(grads);
      mlx_closure_value_and_grad_free(vg);
    } else {
      CK(mlx_closure_apply(&vals, f, in));
      mlx_vector_array_get(&l, vals, 0);
    }
    sum += scalar(l);
    mlx_array_free(l);
    mlx_vector_array_free(vals);
    mlx_vector_array_free(in);
    mlx_array_free(hc);
    mlx_closure_free(f);
    pool_free();
  }
  mlx_vector_array_free(hv);
  mlx_vector_array_free(tv);
  mlx_vector_array_free(mv);
  if (G) {
    mlx_array* dh = TP();
    CK(mlx_concatenate_axis(dh, dhv, 0, S));
    mlx_array dx = keep(mul(rs(*dh, sh3, 3), sc(1.0f / den)));
    CK(mlx_array_eval(dx));
    pool_free();
    for (int j = nl - 1; j >= 0; j--) {
      int li = first + j;
      mlx_closure f = mlx_closure_new_func_payload(layer_closure, (void*)(intptr_t)li, NULL);
      mlx_vector_array primals = mlx_vector_array_new_value(xs[j]), cots = mlx_vector_array_new_value(dx);
      for (int t = 0; t < NTARGET; t++) {
        int i = param_index(li, t);
        if (i < 0) continue;
        mlx_vector_array_append_value(primals, prm[i]);
        mlx_vector_array_append_value(primals, prm[i + 1]);
      }
      mlx_vector_array outs = mlx_vector_array_new(), vjps = mlx_vector_array_new();
      CK(mlx_vjp(&outs, &vjps, f, primals, cots));
      mlx_vector_array ev = mlx_vector_array_new();
      size_t at = 1;
      for (int t = 0; t < NTARGET; t++) {
        int i = param_index(li, t);
        if (i < 0) continue;
        G[i] = mlx_array_new();
        G[i + 1] = mlx_array_new();
        mlx_vector_array_get(&G[i], vjps, at++);
        mlx_vector_array_get(&G[i + 1], vjps, at++);
        mlx_vector_array_append_value(ev, G[i]);
        mlx_vector_array_append_value(ev, G[i + 1]);
      }
      mlx_array below = mlx_array_new();
      if (j > 0) {
        mlx_vector_array_get(&below, vjps, 0);
        mlx_vector_array_append_value(ev, below);
      }
      CK(mlx_eval(ev));
      mlx_vector_array_free(ev);
      mlx_array_free(dx);
      dx = below;
      mlx_vector_array_free(primals);
      mlx_vector_array_free(cots);
      mlx_vector_array_free(outs);
      mlx_vector_array_free(vjps);
      mlx_closure_free(f);
      mlx_array_free(xs[j + 1]);
      xs[j + 1] = (mlx_array){0};
      pool_free();
    }
    mlx_array_free(dx);
  }
  mlx_vector_array_free(dhv);
  for (int j = 0; j <= nl; j++)
    if (xs[j].ctx) mlx_array_free(xs[j]);
  free(xs);
  return (float)(sum / den);
}

/* ─── data ───────────────────────────────────────────────────────────── */

typedef struct {
  uint32_t rows, vocab;
  uint64_t ntok;
  uint32_t *lens, *prompts, *tok;
  uint64_t* off;
  void* blob;
} Tok;
typedef struct {
  uint32_t rows, per_step, steps;
  int64_t seed;
  uint32_t* idx;
  void* blob;
} Order;

static void* slurp(const char* path, size_t* n) {
  FILE* f = fopen(path, "rb");
  if (!f) return NULL;
  fseek(f, 0, SEEK_END);
  long sz = ftell(f);
  fseek(f, 0, SEEK_SET);
  void* b = malloc(sz > 0 ? (size_t)sz : 1);
  if (!b || (sz > 0 && fread(b, 1, (size_t)sz, f) != (size_t)sz)) {
    fclose(f);
    free(b);
    return NULL;
  }
  fclose(f);
  *n = (size_t)sz;
  return b;
}

static uint32_t rd32(const uint8_t* p) { return (uint32_t)p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 | (uint32_t)p[3] << 24; }
static uint64_t rd64(const uint8_t* p) { return (uint64_t)rd32(p) | (uint64_t)rd32(p + 4) << 32; }

static Tok tok_load(const char* path) {
  Tok t = {0};
  size_t n = 0;
  uint8_t* b = slurp(path, &n);
  if (!b) fail("DatasetInvalid", "cannot read %s; the run is not tokenized", path);
  if (n < 24 || memcmp(b, "SKYTOK01", 8) != 0) fail("DatasetInvalid", "%s is not a SKYTOK01 file", path);
  t.rows = rd32(b + 8);
  t.vocab = rd32(b + 12);
  t.ntok = rd64(b + 16);
  size_t need = 24 + (size_t)t.rows * 8 + (size_t)t.ntok * 4;
  if (n != need) fail("DatasetInvalid", "%s is %zu bytes and its header says %zu", path, n, need);
  t.lens = malloc((size_t)t.rows * 4 + 4);
  t.prompts = malloc((size_t)t.rows * 4 + 4);
  t.off = malloc((size_t)t.rows * 8 + 8);
  t.tok = (uint32_t*)(b + 24 + (size_t)t.rows * 8); /* little-endian, 4-aligned */
  uint64_t at = 0;
  for (uint32_t i = 0; i < t.rows; i++) {
    t.lens[i] = rd32(b + 24 + 8 * (size_t)i);
    t.prompts[i] = rd32(b + 28 + 8 * (size_t)i);
    t.off[i] = at;
    at += t.lens[i];
  }
  if (at != t.ntok) fail("DatasetInvalid", "%s: row lengths sum to %llu, header says %llu", path, (unsigned long long)at, (unsigned long long)t.ntok);
  for (uint64_t k = 0; k < t.ntok; k++)
    if (t.tok[k] >= (uint32_t)C.vocab)
      fail("InvalidSetting", "%s holds token %u and the base has %d", path, t.tok[k], C.vocab);
  t.blob = b;
  return t;
}

static Order order_load(const char* path) {
  Order o = {0};
  size_t n = 0;
  uint8_t* b = slurp(path, &n);
  if (!b) fail("DatasetInvalid", "cannot read %s; the run is not tokenized", path);
  if (n < 32 || memcmp(b, "SKYORD01", 8) != 0) fail("DatasetInvalid", "%s is not a SKYORD01 file", path);
  o.rows = rd32(b + 8);
  o.per_step = rd32(b + 12);
  o.steps = rd32(b + 16);
  o.seed = (int64_t)rd64(b + 24);
  if (n != 32 + (size_t)o.per_step * o.steps * 4) fail("DatasetInvalid", "%s has the wrong size", path);
  o.idx = (uint32_t*)(b + 32);
  o.blob = b;
  return o;
}

/* A micro-batch of rows as model inputs. Returns the token count. */
static long make_batch(const Tok* t, const uint32_t* rows, int n, mlx_array* ids, mlx_array* tg, mlx_array* mk,
                       float* nmask) {
  int len = 2;
  for (int i = 0; i < n; i++)
    if ((int)t->lens[rows[i]] > len) len = (int)t->lens[rows[i]];
  int w = len - 1;
  int32_t* bi = calloc((size_t)n * w, 4);
  int32_t* bt = calloc((size_t)n * w, 4);
  float* bm = calloc((size_t)n * w, 4);
  long toks = 0;
  float m = 0;
  for (int i = 0; i < n; i++) {
    uint32_t r = rows[i];
    if (r >= t->rows) fail("DatasetInvalid", "order.bin names row %u of %u", r, t->rows);
    const uint32_t* x = t->tok + t->off[r];
    int ln = (int)t->lens[r], p = (int)t->prompts[r];
    int lo = p > 0 ? p - 1 : 0;
    toks += ln;
    for (int k = 0; k < w; k++) {
      bi[i * w + k] = k < ln ? (int32_t)x[k] : 0;
      bt[i * w + k] = k + 1 < ln ? (int32_t)x[k + 1] : 0;
      bm[i * w + k] = (k >= lo && k < ln - 1) ? 1.0f : 0.0f;
      m += bm[i * w + k];
    }
  }
  int sh[2] = {n, w};
  *ids = mlx_array_new_data(bi, sh, 2, MLX_INT32);
  *tg = mlx_array_new_data(bt, sh, 2, MLX_INT32);
  *mk = mlx_array_new_data(bm, sh, 2, MLX_FLOAT32);
  free(bi);
  free(bt);
  free(bm);
  if (nmask) *nmask = m;
  return toks;
}

/* ─── safetensors (F32), written byte-for-byte deterministically ─────── */

typedef struct {
  const char* name;
  mlx_array a;
} Named;
static int named_cmp(const void* x, const void* y) { return strcmp(((const Named*)x)->name, ((const Named*)y)->name); }

static void fsync_path(const char* path) {
  int fd = open(path, O_RDONLY);
  if (fd >= 0) {
    fsync(fd);
    close(fd);
  }
}

static void write_safetensors(const char* path, Named* ts, int n) {
  qsort(ts, (size_t)n, sizeof *ts, named_cmp);
  mlx_array* host = calloc((size_t)n, sizeof *host);
  for (int i = 0; i < n; i++) {
    mlx_array f = mlx_array_new();
    host[i] = mlx_array_new();
    CK(mlx_astype(&f, ts[i].a, MLX_FLOAT32, S));
    CK(mlx_contiguous(&host[i], f, false, S));
    mlx_array_free(f);
  }
  eval_arrays(host, n);
  size_t hcap = 256 + (size_t)n * 512, ho = 0;
  char* hdr = malloc(hcap);
  ho += (size_t)snprintf(hdr + ho, hcap - ho, "{\"__metadata__\":{\"format\":\"pt\"}");
  uint64_t off = 0;
  for (int i = 0; i < n; i++) {
    size_t nd = mlx_array_ndim(host[i]);
    const int* sh = mlx_array_shape(host[i]);
    uint64_t bytes = (uint64_t)mlx_array_size(host[i]) * 4;
    ho += (size_t)snprintf(hdr + ho, hcap - ho, ",\"%s\":{\"data_offsets\":[%llu,%llu],\"dtype\":\"F32\",\"shape\":[", ts[i].name,
                           (unsigned long long)off, (unsigned long long)(off + bytes));
    for (size_t d = 0; d < nd; d++) ho += (size_t)snprintf(hdr + ho, hcap - ho, d ? ",%d" : "%d", sh[d]);
    ho += (size_t)snprintf(hdr + ho, hcap - ho, "]}");
    off += bytes;
  }
  ho += (size_t)snprintf(hdr + ho, hcap - ho, "}");
  while (ho % 8) hdr[ho++] = ' ';
  FILE* f = fopen(path, "wb");
  if (!f) fail("TrainingFailed", "cannot write %s: %s", path, strerror(errno));
  uint8_t lenb[8];
  for (int i = 0; i < 8; i++) lenb[i] = (uint8_t)((uint64_t)ho >> (8 * i));
  fwrite(lenb, 1, 8, f);
  fwrite(hdr, 1, ho, f);
  for (int i = 0; i < n; i++) fwrite(mlx_array_data_float32(host[i]), 4, mlx_array_size(host[i]), f);
  if (fflush(f) != 0 || fsync(fileno(f)) != 0 || ferror(f)) fail("TrainingFailed", "cannot write %s", path);
  fclose(f);
  for (int i = 0; i < n; i++) mlx_array_free(host[i]);
  free(host);
  free(hdr);
}

static void write_text(const char* path, const char* text) {
  FILE* f = fopen(path, "wb");
  if (!f) fail("TrainingFailed", "cannot write %s: %s", path, strerror(errno));
  fputs(text, f);
  if (fflush(f) != 0 || fsync(fileno(f)) != 0) fail("TrainingFailed", "cannot write %s", path);
  fclose(f);
}

static void rm_tree_flat(const char* dir) {
  /* Our temporary directories hold plain files only. */
  const char* names[] = {"adapter.safetensors", "optimizer.safetensors", "state.json", "manifest.json",
                         "adapter_model.safetensors", "adapter_config.json"};
  char p[4096];
  for (size_t i = 0; i < sizeof names / sizeof *names; i++) {
    snprintf(p, sizeof p, "%s/%s", dir, names[i]);
    unlink(p);
  }
  rmdir(dir);
}

/* ─── checkpoints ────────────────────────────────────────────────────── */

static void save_params(const char* dir) {
  char p[4096];
  Named* ts = calloc((size_t)NP * 2 + 1, sizeof *ts);
  for (int i = 0; i < NP; i++) ts[i] = (Named){PNAME[i], P[i]};
  snprintf(p, sizeof p, "%s/adapter.safetensors", dir);
  write_safetensors(p, ts, NP);
  char(*mn)[170] = calloc((size_t)NP * 2, sizeof *mn);
  for (int i = 0; i < NP; i++) {
    snprintf(mn[2 * i], sizeof mn[0], "m.%s", PNAME[i]);
    snprintf(mn[2 * i + 1], sizeof mn[0], "v.%s", PNAME[i]);
    ts[2 * i] = (Named){mn[2 * i], M1[i]};
    ts[2 * i + 1] = (Named){mn[2 * i + 1], M2[i]};
  }
  snprintf(p, sizeof p, "%s/optimizer.safetensors", dir);
  write_safetensors(p, ts, NP * 2);
  free(mn);
  free(ts);
}

static void write_checkpoint(int step, double loss) {
  char root[4096], fin[4096], tmp[4096], p[4096], name[16];
  snprintf(root, sizeof root, "%s/checkpoints", C.run);
  mkdir(root, 0755);
  snprintf(name, sizeof name, "%07d", step);
  snprintf(fin, sizeof fin, "%s/%s", root, name);
  struct stat st;
  if (stat(fin, &st) == 0) return;
  snprintf(tmp, sizeof tmp, "%s/tmp-%s-%d", root, name, (int)getpid());
  rm_tree_flat(tmp);
  if (mkdir(tmp, 0755) != 0) fail("TrainingFailed", "cannot create %s: %s", tmp, strerror(errno));
  save_params(tmp);
  char ls[64], state[256];
  json_num(ls, sizeof ls, loss);
  snprintf(state, sizeof state, "{\"attempt\":%d,\"loss\":%s,\"step\":%d}", C.attempt, ls, step);
  snprintf(p, sizeof p, "%s/state.json", tmp);
  write_text(p, state);
  const char* files[] = {"adapter.safetensors", "optimizer.safetensors", "state.json"};
  char man[1024];
  size_t mo = (size_t)snprintf(man, sizeof man, "{\"files\":{");
  for (int i = 0; i < 3; i++) {
    char hex[65];
    snprintf(p, sizeof p, "%s/%s", tmp, files[i]);
    if (sky_sha256_file_hex(p, hex) != 0) fail("TrainingFailed", "cannot hash %s", p);
    mo += (size_t)snprintf(man + mo, sizeof man - mo, "%s\"%s\":\"%s\"", i ? "," : "", files[i], hex);
  }
  snprintf(man + mo, sizeof man - mo, "},\"step\":%d}", step);
  snprintf(p, sizeof p, "%s/manifest.json", tmp);
  write_text(p, man);
  fsync_path(tmp);
  if (rename(tmp, fin) != 0) fail("TrainingFailed", "cannot rename %s: %s", tmp, strerror(errno));
  fsync_path(root);
  char line[512];
  snprintf(line, sizeof line, "{\"path\":\"checkpoints/%s\",\"step\":%d,\"type\":\"checkpoint\",\"v\":1}", name, step);
  emit(line);
}

/* Verifies a checkpoint's manifest, then loads its parameters and moments. */
static void load_checkpoint(const char* dir) {
  char p[4096];
  size_t n = 0;
  snprintf(p, sizeof p, "%s/manifest.json", dir);
  char* man = slurp(p, &n);
  if (!man) fail("TrainingFailed", "checkpoint %s has no manifest", dir);
  man = realloc(man, n + 1);
  man[n] = 0;
  char* files = strstr(man, "\"files\":{");
  if (!files) fail("TrainingFailed", "checkpoint %s has a manifest without files", dir);
  char* s = files + 9;
  int seen = 0;
  while (*s && *s != '}') {
    char key[256], val[80];
    if (sscanf(s, " \"%255[^\"]\" : \"%79[^\"]\"", key, val) != 2) break;
    char hex[65];
    snprintf(p, sizeof p, "%s/%s", dir, key);
    if (sky_sha256_file_hex(p, hex) != 0 || strcmp(hex, val) != 0)
      fail("TrainingFailed", "checkpoint %s does not match its manifest (%s)", dir, key);
    seen++;
    char* q = strchr(s, '"');
    q = q ? strchr(q + 1, '"') : NULL; /* end of key */
    q = q ? strchr(q + 1, '"') : NULL; /* start of value */
    q = q ? strchr(q + 1, '"') : NULL; /* end of value */
    if (!q) break;
    s = q + 1;
    while (*s == ',' || *s == ' ') s++;
  }
  free(man);
  if (seen < 3) fail("TrainingFailed", "checkpoint %s lists %d files", dir, seen);
  mlx_stream cpu = mlx_default_cpu_stream_new();
  const char* parts[2] = {"adapter.safetensors", "optimizer.safetensors"};
  for (int k = 0; k < 2; k++) {
    mlx_map_string_to_array m = mlx_map_string_to_array_new();
    mlx_map_string_to_string md = mlx_map_string_to_string_new();
    snprintf(p, sizeof p, "%s/%s", dir, parts[k]);
    if (mlx_load_safetensors(&m, &md, p, cpu) != 0) fail("TrainingFailed", "cannot read %s: %s", p, g_mlx_err);
    for (int i = 0; i < NP; i++) {
      char key[200];
      mlx_array* dst = k == 0 ? &P[i] : &M1[i];
      for (int pass = 0; pass < (k == 0 ? 1 : 2); pass++) {
        if (k == 0)
          snprintf(key, sizeof key, "%s", PNAME[i]);
        else
          snprintf(key, sizeof key, "%s.%s", pass ? "v" : "m", PNAME[i]);
        if (k == 1) dst = pass ? &M2[i] : &M1[i];
        mlx_array a = mlx_array_new();
        if (mlx_map_string_to_array_get(&a, m, key) != 0) fail("TrainingFailed", "checkpoint %s has no %s", dir, key);
        mlx_array_set(dst, a);
        mlx_array_free(a);
      }
    }
    mlx_map_string_to_array_free(m);
    mlx_map_string_to_string_free(md);
  }
  mlx_stream_free(cpu);
  eval_arrays(P, NP);
  eval_arrays(M1, NP);
  eval_arrays(M2, NP);
}

static void export_adapter(void) {
  char out[4096], tmp[4096], p[4096];
  snprintf(out, sizeof out, "%s/adapter", C.run);
  snprintf(tmp, sizeof tmp, "%s/adapter.tmp-%d", C.run, (int)getpid());
  rm_tree_flat(tmp);
  if (mkdir(tmp, 0755) != 0) fail("TrainingFailed", "cannot create %s: %s", tmp, strerror(errno));
  Named* ts = calloc((size_t)NP + 1, sizeof *ts);
  for (int i = 0; i < NP; i++) ts[i] = (Named){PNAME[i], P[i]};
  snprintf(p, sizeof p, "%s/adapter_model.safetensors", tmp);
  write_safetensors(p, ts, NP);
  free(ts);
  char conf[2048], targets[512] = "", layers[1024] = "", qbase[512];
  size_t to = 0, lo = 0;
  for (int t = 0; t < NTARGET; t++)
    if (C.targets & (1u << t)) to += (size_t)snprintf(targets + to, sizeof targets - to, "%s\"%s\"", to ? "," : "", TNAME[t]);
  for (int li = first_lora_layer(); li < C.layers; li++)
    lo += (size_t)snprintf(layers + lo, sizeof layers - lo, "%s%d", lo ? "," : "", li);
  json_str(qbase, sizeof qbase, C.base_id ? C.base_id : "");
  snprintf(conf, sizeof conf,
           "{\"base_model_name_or_path\":%s,\"bias\":\"none\",\"fan_in_fan_out\":false,\"layers_to_transform\":[%s],"
           "\"lora_alpha\":%g,\"lora_dropout\":0.0,\"peft_type\":\"LORA\",\"r\":%d,\"target_modules\":[%s],"
           "\"task_type\":\"CAUSAL_LM\"}\n",
           qbase, layers, (double)C.alpha, C.rank, targets);
  snprintf(p, sizeof p, "%s/adapter_config.json", tmp);
  write_text(p, conf);
  fsync_path(tmp);
  rm_tree_flat(out);
  if (rename(tmp, out) != 0) fail("TrainingFailed", "cannot rename %s: %s", tmp, strerror(errno));
  fsync_path(C.run);
}

/* ─── training ───────────────────────────────────────────────────────── */

static volatile sig_atomic_t g_term = 0;
static void on_term(int sig) {
  (void)sig;
  g_term = 1;
}
static int cancel_requested(void) {
  char p[4096];
  struct stat st;
  snprintf(p, sizeof p, "%s/cancel", C.run);
  return g_term || stat(p, &st) == 0;
}

static double now_s(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

static mlx_closure g_loss_p;
static mlx_closure_value_and_grad g_vg;

static mlx_vector_array inputs(const mlx_array* prm, mlx_array ids, mlx_array tg, mlx_array mk) {
  mlx_vector_array in = mlx_vector_array_new();
  if (prm)
    for (int i = 0; i < NP; i++) mlx_vector_array_append_value(in, prm[i]);
  mlx_vector_array_append_value(in, ids);
  mlx_vector_array_append_value(in, tg);
  mlx_vector_array_append_value(in, mk);
  return in;
}

static float apply_loss(mlx_closure cls, const mlx_array* prm, mlx_array ids, mlx_array tg, mlx_array mk) {
  mlx_vector_array in = inputs(prm, ids, tg, mk), out = mlx_vector_array_new();
  CK(mlx_closure_apply(&out, cls, in));
  mlx_array l = mlx_array_new();
  mlx_vector_array_get(&l, out, 0);
  CK(mlx_array_eval(l));
  float v = 0;
  CK(mlx_array_item_float32(&v, l));
  mlx_array_free(l);
  mlx_vector_array_free(in);
  mlx_vector_array_free(out);
  return v;
}

/* The mean masked NLL of one micro-batch: chunked unless --loss-chunk 0.
 * With G (NP handles), G receives the gradient over P, lazily: the caller
 * evaluates it and then frees the temporaries. */
static float micro_loss(const mlx_array* prm, mlx_array ids, mlx_array tg, mlx_array mk, mlx_array* G) {
  int k = loss_chunk_tokens();
  if (k > 0) return chunked_loss(prm, ids, tg, mk, k, G);
  if (!G) return apply_loss(g_loss_p, prm, ids, tg, mk);
  mlx_vector_array in = inputs(prm, ids, tg, mk), vals = mlx_vector_array_new(), grads = mlx_vector_array_new();
  CK(mlx_closure_value_and_grad_apply(&vals, &grads, g_vg, in));
  for (int i = 0; i < NP; i++) {
    G[i] = mlx_array_new();
    mlx_vector_array_get(&G[i], grads, (size_t)i);
  }
  mlx_array l = mlx_array_new();
  mlx_vector_array_get(&l, vals, 0);
  mlx_vector_array ev = mlx_vector_array_new_data(G, (size_t)NP);
  mlx_vector_array_append_value(ev, l);
  CK(mlx_eval(ev));
  mlx_vector_array_free(ev);
  float v = scalar(l);
  mlx_array_free(l);
  mlx_vector_array_free(vals);
  mlx_vector_array_free(grads);
  mlx_vector_array_free(in);
  return v;
}

/* The mean masked NLL over the eval rows, weighted by their target tokens. */
static double evaluate(const Tok* valid) {
  int rows = (int)valid->rows;
  if (C.max_eval_rows > 0 && rows > C.max_eval_rows) rows = C.max_eval_rows;
  double num = 0, den = 0;
  uint32_t* idx = malloc((size_t)C.batch * 4);
  for (int r = 0; r < rows; r += C.batch) {
    int n = rows - r < C.batch ? rows - r : C.batch;
    for (int i = 0; i < n; i++) idx[i] = (uint32_t)(r + i);
    mlx_array ids, tg, mk;
    float m = 0;
    make_batch(valid, idx, n, &ids, &tg, &mk, &m);
    float l = micro_loss(P, ids, tg, mk, NULL);
    mlx_array_free(ids);
    mlx_array_free(tg);
    mlx_array_free(mk);
    pool_free();
    num += (double)l * m;
    den += m;
  }
  free(idx);
  return den > 0 ? num / den : NAN;
}

static void usage(void) {
  fputs(
      "usage: sky-trainer --selftest [--cpu]\n"
      "       sky-trainer --run DIR --model DIR [flags]\n"
      "shape:    --layers N --hidden N --heads N --kv-heads N --intermediate N --vocab N\n"
      "          --rope-theta F --rms-eps F --tied 0|1 [--weights FILE]... [--base-id ID]\n"
      "          [--base-bits B --base-group-size G]   (an MLX-quantised base)\n"
      "lora:     --rank N --alpha F --lora-layers N --targets q_proj,v_proj,...\n"
      "train:    --lr F [--weight-decay F] --batch N --accumulate N --checkpoint-every N\n"
      "          --eval-every N [--max-eval-rows N] --seed N\n"
      "memory:   [--quant-bits 0|4|8] [--quant-group-size G]\n"
      "          [--memory-cap BYTES] [--cache-limit BYTES]\n"
      "run:      [--cpu] [--attempt N] [--resume-step N --resume-dir REL] [--launch TOKEN]\n"
      "tests:    [--crash-at N] [--pace-ms N] [--quantise-snapshot OUT]\n"
      "          [--loss-chunk TOKENS (0: unchunked)] [--grad-checkpoint 0|1 (the unchunked loss)]\n",
      stderr);
}

static void parse_targets(const char* s) {
  C.targets = 0;
  char buf[256];
  snprintf(buf, sizeof buf, "%s", s);
  for (char* tok = strtok(buf, ","); tok; tok = strtok(NULL, ",")) {
    int found = 0;
    for (int t = 0; t < NTARGET; t++)
      if (strcmp(tok, TNAME[t]) == 0) {
        C.targets |= 1u << t;
        found = 1;
      }
    if (!found) fail("InvalidSetting", "--targets names %s, which is not a Qwen2 projection", tok);
  }
  if (!C.targets) fail("InvalidSetting", "--targets is empty");
}

static uint64_t parse_u64(const char* flag, const char* v) {
  char* end;
  errno = 0;
  unsigned long long x = strtoull(v, &end, 10);
  if (errno || *end || end == v) fail("InvalidSetting", "%s takes a whole number", flag);
  return (uint64_t)x;
}
static int parse_int(const char* flag, const char* v) {
  uint64_t x = parse_u64(flag, v);
  if (x > 1u << 30) fail("InvalidSetting", "%s is too large", flag);
  return (int)x;
}
static float parse_float(const char* flag, const char* v) {
  char* end;
  float x = strtof(v, &end);
  if (*end || end == v || !isfinite(x)) fail("InvalidSetting", "%s takes a number", flag);
  return x;
}

static void defaults(void) {
  memset(&C, 0, sizeof C);
  C.rope_theta = 1e6f;
  C.rms_eps = 1e-6f;
  C.rank = 8;
  C.alpha = 16;
  C.targets = (1u << T_Q) | (1u << T_V);
  C.lr = 1e-4f;
  C.wd = 0.0f;
  C.batch = 1;
  C.accumulate = 1;
  C.quant_group = 64;
  C.base_group = 64;
  C.base_bits = 4;
  C.loss_chunk = -1;
}

static int selftest(void);
static void quantise_snapshot(const char* out);

int main(int argc, char** argv) {
  defaults();
  int self = 0;
  const char* quant_out = NULL;
  for (int i = 1; i < argc; i++) {
    const char* a = argv[i];
    const char* v = i + 1 < argc ? argv[i + 1] : NULL;
#define FLAG(name) (strcmp(a, name) == 0 && (v ? (i++, 1) : (fail("InvalidSetting", "%s needs a value", name), 0)))
    if (strcmp(a, "--selftest") == 0) self = 1;
    else if (strcmp(a, "--cpu") == 0) C.use_cpu = 1;
    else if (strcmp(a, "--help") == 0) { usage(); return 0; }
    else if (strcmp(a, "--version") == 0) { printf("sky-trainer %s (mlx-c)\n", SKY_TRAINER_VERSION); return 0; }
    else if (FLAG("--run")) C.run = v;
    else if (FLAG("--model")) C.model = v;
    else if (FLAG("--base-id")) C.base_id = v;
    else if (FLAG("--weights")) { if (C.nweights < 64) C.weights[C.nweights++] = v; }
    else if (FLAG("--layers")) C.layers = parse_int(a, v);
    else if (FLAG("--hidden")) C.hidden = parse_int(a, v);
    else if (FLAG("--heads")) C.heads = parse_int(a, v);
    else if (FLAG("--kv-heads")) C.kv_heads = parse_int(a, v);
    else if (FLAG("--intermediate")) C.inter = parse_int(a, v);
    else if (FLAG("--vocab")) C.vocab = parse_int(a, v);
    else if (FLAG("--tied")) C.tied = parse_int(a, v);
    else if (FLAG("--rope-theta")) C.rope_theta = parse_float(a, v);
    else if (FLAG("--rms-eps")) C.rms_eps = parse_float(a, v);
    else if (FLAG("--rank")) C.rank = parse_int(a, v);
    else if (FLAG("--alpha")) C.alpha = parse_float(a, v);
    else if (FLAG("--lora-layers")) C.lora_layers = parse_int(a, v);
    else if (FLAG("--targets")) parse_targets(v);
    else if (FLAG("--lr")) C.lr = parse_float(a, v);
    else if (FLAG("--weight-decay")) C.wd = parse_float(a, v);
    else if (FLAG("--batch")) C.batch = parse_int(a, v);
    else if (FLAG("--accumulate")) C.accumulate = parse_int(a, v);
    else if (FLAG("--checkpoint-every")) C.ckpt_every = parse_int(a, v);
    else if (FLAG("--eval-every")) C.eval_every = parse_int(a, v);
    else if (FLAG("--max-eval-rows")) C.max_eval_rows = parse_int(a, v);
    else if (FLAG("--seed")) C.seed = parse_u64(a, v);
    else if (FLAG("--quant-bits")) C.quant_bits = parse_int(a, v);
    else if (FLAG("--quant-group-size")) C.quant_group = parse_int(a, v);
    else if (FLAG("--base-bits")) C.base_bits = parse_int(a, v);
    else if (FLAG("--base-group-size")) C.base_group = parse_int(a, v);
    else if (FLAG("--grad-checkpoint")) C.grad_ckpt = parse_int(a, v);
    else if (FLAG("--loss-chunk")) C.loss_chunk = parse_int(a, v);
    else if (FLAG("--memory-cap")) C.cap = parse_u64(a, v);
    else if (FLAG("--cache-limit")) C.cache_limit = parse_u64(a, v);
    else if (FLAG("--attempt")) C.attempt = parse_int(a, v);
    else if (FLAG("--resume-step")) C.resume_step = parse_int(a, v);
    else if (FLAG("--resume-dir")) C.resume_dir = v;
    /* The launch token is the trainer's identity on its command line: the
       runtime finds a running trainer by it, never by its pid alone. */
    else if (FLAG("--launch")) (void)v;
    else if (FLAG("--crash-at")) C.crash_at = parse_int(a, v);
    else if (FLAG("--pace-ms")) C.pace_ms = parse_int(a, v);
    else if (FLAG("--quantise-snapshot")) quant_out = v;
    else {
      usage();
      fail("InvalidSetting", "unknown flag %s", a);
    }
#undef FLAG
  }
  /* The guard first: before MLX allocates anything or touches Metal. */
  if (C.cap > 0) {
    g_guard = sky_guard_start(C.run ? C.run : ".", C.cap);
    if (g_guard < 0) fail("TrainingFailed", "cannot start the memory guard: %s", strerror(errno));
  }
  mlx_set_error_handler(on_mlx_error, NULL, NULL);
  if (!C.use_cpu) {
    bool metal = false;
    if (mlx_metal_is_available(&metal) != 0 || !metal)
      fail("NoAccelerator", "Metal is not available here; run with --cpu");
  }
  S = C.use_cpu ? mlx_default_cpu_stream_new() : mlx_default_gpu_stream_new();
  if (C.use_cpu) {
    mlx_device cpu = mlx_device_new_type(MLX_CPU, 0);
    mlx_set_default_device(cpu);
    mlx_device_free(cpu);
  }
  size_t prev = 0;
  if (C.cache_limit > 0) mlx_set_cache_limit(&prev, (size_t)C.cache_limit);
  if (C.cap > 0) mlx_set_memory_limit(&prev, (size_t)(C.cap / 10 * 8));
  if (self) {
    int rc = selftest();
    sky_guard_stop(g_guard);
    return rc;
  }

  if (!C.model) fail("InvalidSetting", "--model is required");
  if (!C.layers || !C.hidden || !C.heads || !C.kv_heads || !C.inter || !C.vocab)
    fail("InvalidSetting", "the shape flags --layers --hidden --heads --kv-heads --intermediate --vocab are required");
  if (C.hidden % C.heads != 0 || C.heads % C.kv_heads != 0)
    fail("InvalidSetting", "--hidden must divide by --heads and --heads by --kv-heads");
  C.head_dim = C.hidden / C.heads;
  if (C.quant_bits != 0 && C.quant_bits != 4 && C.quant_bits != 8) fail("InvalidSetting", "--quant-bits is 0, 4 or 8");
  if (quant_out) {
    quantise_snapshot(quant_out);
    return 0;
  }
  if (!C.run) fail("InvalidSetting", "--run is required");
  if (C.rank < 1 || C.batch < 1 || C.accumulate < 1) fail("InvalidSetting", "--rank, --batch and --accumulate are at least 1");
  signal(SIGTERM, on_term);

  char p[4096];
  snprintf(p, sizeof p, "%s/data/train.tok", C.run);
  Tok train = tok_load(p);
  snprintf(p, sizeof p, "%s/data/valid.tok", C.run);
  Tok valid = tok_load(p);
  snprintf(p, sizeof p, "%s/data/order.bin", C.run);
  Order order = order_load(p);
  if (train.rows == 0) fail("DatasetInvalid", "the train split is empty");
  if (valid.rows == 0) fail("DatasetInvalid", "the eval split is empty");
  if (order.rows != train.rows || order.per_step != (uint32_t)(C.batch * C.accumulate))
    fail("InvalidSetting", "order.bin was written for %u rows of %u per step; the run has %u rows of %d", order.rows,
         order.per_step, train.rows, C.batch * C.accumulate);
  int total = (int)order.steps, start = C.resume_step;
  if (start < 0 || start > total) fail("InvalidSetting", "--resume-step %d is outside 0..%d", start, total);

  load_model();
  params_alloc();
  params_init(C.seed);
  if (C.grad_ckpt) checkpoint_closures();
  g_loss_p = mlx_closure_new_func_payload(loss_closure, (void*)1, NULL);
  int* argn = malloc((size_t)NP * sizeof *argn);
  for (int i = 0; i < NP; i++) argn[i] = i;
  g_vg = mlx_closure_value_and_grad_new();
  CK(mlx_value_and_grad(&g_vg, g_loss_p, argn, (size_t)NP));

  {
    char line[1024];
    snprintf(line, sizeof line,
             "{\"attempt\":%d,\"backend\":\"mlx\",\"pid\":%d,\"resumeFrom\":%d,\"toolchain\":\"native\","
             "\"totalSteps\":%d,\"type\":\"hello\",\"v\":1,\"versions\":{\"mlx\":\"%s\",\"sky-trainer\":\"%s\"}}",
             C.attempt, (int)getpid(), start, total, SKY_MLX_VERSION, SKY_TRAINER_VERSION);
    emit(line);
  }
  if (start > 0) {
    char dir[4096];
    if (C.resume_dir && C.resume_dir[0] == '/')
      snprintf(dir, sizeof dir, "%s", C.resume_dir);
    else if (C.resume_dir)
      snprintf(dir, sizeof dir, "%s/%s", C.run, C.resume_dir);
    else
      snprintf(dir, sizeof dir, "%s/checkpoints/%07d", C.run, start);
    load_checkpoint(dir);
  }

  int per = C.batch * C.accumulate;
  int epoch_len = ((int)train.rows + per - 1) / per;
  if (epoch_len < 1) epoch_len = 1;
  int last_ckpt = start;
  double last_loss = NAN;
  const float B1 = 0.9f, B2 = 0.999f, AEPS = 1e-8f;
  for (int step = start; step < total; step++) {
    if (cancel_requested()) {
      if (step > last_ckpt) write_checkpoint(step, last_loss);
      char line[128];
      snprintf(line, sizeof line, "{\"step\":%d,\"type\":\"cancelled\",\"v\":1}", step);
      emit(line);
      sky_guard_stop(g_guard);
      return 0;
    }
    double t0 = now_s();
    mlx_array* G = calloc((size_t)NP, sizeof *G);
    double loss_sum = 0;
    long toks = 0;
    for (int a = 0; a < C.accumulate; a++) {
      const uint32_t* rows = order.idx + (size_t)step * per + (size_t)a * C.batch;
      mlx_array ids, tg, mk;
      toks += make_batch(&train, rows, C.batch, &ids, &tg, &mk, NULL);
      mlx_array* gm = calloc((size_t)NP, sizeof *gm);
      float lv = micro_loss(P, ids, tg, mk, gm);
      for (int i = 0; i < NP; i++) {
        mlx_array g = gm[i];
        if (a == 0) {
          G[i] = g;
        } else {
          mlx_array s = mlx_array_new();
          CK(mlx_add(&s, G[i], g, S));
          mlx_array_free(G[i]);
          mlx_array_free(g);
          G[i] = s;
        }
      }
      free(gm);
      mlx_vector_array ev = mlx_vector_array_new_data(G, (size_t)NP);
      CK(mlx_eval(ev));
      mlx_vector_array_free(ev);
      loss_sum += lv;
      mlx_array_free(ids);
      mlx_array_free(tg);
      mlx_array_free(mk);
      pool_free();
    }
    /* AdamW, bias-corrected, decoupled weight decay, constant rate. */
    float c1 = 1.0f - powf(B1, (float)(step + 1)), c2 = 1.0f - powf(B2, (float)(step + 1));
    float ga = 1.0f / (float)C.accumulate;
    mlx_vector_array ev = mlx_vector_array_new();
    for (int i = 0; i < NP; i++) {
      mlx_array g = C.accumulate > 1 ? mul(G[i], sc(ga)) : G[i];
      mlx_array m = add(mul(sc(B1), M1[i]), mul(sc(1 - B1), g));
      mlx_array v = add(mul(sc(B2), M2[i]), mul(sc(1 - B2), sq(g)));
      mlx_array upd = dv(dv(m, sc(c1)), add(sqr(dv(v, sc(c2))), sc(AEPS)));
      mlx_array np_ = sub(mul(P[i], sc(1 - C.lr * C.wd)), mul(sc(C.lr), upd));
      mlx_array_set(&M1[i], m);
      mlx_array_set(&M2[i], v);
      mlx_array_set(&P[i], np_);
      mlx_vector_array_append_value(ev, P[i]);
      mlx_vector_array_append_value(ev, M1[i]);
      mlx_vector_array_append_value(ev, M2[i]);
    }
    CK(mlx_eval(ev));
    mlx_vector_array_free(ev);
    for (int i = 0; i < NP; i++) mlx_array_free(G[i]);
    free(G);
    pool_free();
    int done = step + 1;
    last_loss = loss_sum / C.accumulate;
    double el = now_s() - t0;
    if (el < 1e-6) el = 1e-6;
    {
      char ls[64], lrs[64], tps[64], line[1024];
      json_num(ls, sizeof ls, last_loss);
      json_num(lrs, sizeof lrs, C.lr);
      json_num(tps, sizeof tps, round(toks / el * 1000.0) / 1000.0);
      snprintf(line, sizeof line,
               "{\"epoch\":%d,\"footprintBytes\":%llu,\"learningRate\":%s,\"loss\":%s,\"peakBytes\":%llu,\"step\":%d,"
               "\"tokensPerSecond\":%s,\"total\":%d,\"type\":\"progress\",\"v\":1}",
               step / epoch_len, (unsigned long long)sky_mem_footprint(getpid()),
               lrs, ls, (unsigned long long)sky_mem_peak(getpid()), done, tps, total);
      emit(line);
    }
    if (C.eval_every > 0 && done % C.eval_every == 0 && done < total) {
      char vs[64], line[256];
      json_num(vs, sizeof vs, evaluate(&valid));
      snprintf(line, sizeof line, "{\"name\":\"val_loss\",\"step\":%d,\"type\":\"metric\",\"v\":1,\"value\":%s}", done, vs);
      emit(line);
    }
    if (C.ckpt_every > 0 && done % C.ckpt_every == 0 && done < total) {
      write_checkpoint(done, last_loss);
      last_ckpt = done;
    }
    if (C.pace_ms > 0) {
      /* The test pace: a step that takes this long, so a test can act mid-run. */
      struct timespec ts = {C.pace_ms / 1000, (long)(C.pace_ms % 1000) * 1000000L};
      nanosleep(&ts, NULL);
    }
    if (C.crash_at > 0 && done == C.crash_at) {
      /* The test crash: no terminal line, no cleanup, as a SIGKILL ends it. */
      fflush(stdout);
      _exit(137);
    }
  }
  double val = evaluate(&valid);
  {
    char vs[64], line[256];
    json_num(vs, sizeof vs, val);
    snprintf(line, sizeof line, "{\"name\":\"val_loss\",\"step\":%d,\"type\":\"metric\",\"v\":1,\"value\":%s}", total, vs);
    emit(line);
  }
  export_adapter();
  {
    char hex[65], line[512];
    snprintf(p, sizeof p, "%s/adapter/adapter_model.safetensors", C.run);
    if (sky_sha256_file_hex(p, hex) != 0) fail("TrainingFailed", "cannot hash %s", p);
    snprintf(line, sizeof line, "{\"kind\":\"peft\",\"path\":\"adapter\",\"sha256\":\"%s\",\"type\":\"artefact\",\"v\":1}", hex);
    emit(line);
    char ls[64], vs[64];
    json_num(ls, sizeof ls, last_loss);
    json_num(vs, sizeof vs, val);
    snprintf(line, sizeof line, "{\"step\":%d,\"trainLoss\":%s,\"type\":\"done\",\"v\":1,\"valLoss\":%s}", total, ls, vs);
    emit(line);
  }
  sky_guard_stop(g_guard);
  return 0;
}

/* ─── utilities for tests ────────────────────────────────────────────── */

/* Writes the base, MLX-quantised (weight as packed uint32, scales, biases),
 * to <out>/model.safetensors: the layout mlx-community snapshots use. */
static void quantise_snapshot(const char* out) {
  WM = mlx_map_string_to_array_new();
  mlx_map_string_to_string meta = mlx_map_string_to_string_new();
  char p[4096];
  snprintf(p, sizeof p, "%s/model.safetensors", C.model);
  mlx_stream cpu = mlx_default_cpu_stream_new();
  if (mlx_load_safetensors(&WM, &meta, p, cpu) != 0) fail("InvalidSetting", "cannot read %s: %s", p, g_mlx_err);
  mlx_map_string_to_array q = mlx_map_string_to_array_new();
  mlx_map_string_to_array_iterator it = mlx_map_string_to_array_iterator_new(WM);
  const char* key;
  mlx_array val = mlx_array_new();
  int bits = C.quant_bits ? C.quant_bits : 4;
  while (mlx_map_string_to_array_iterator_next(&key, &val, it) == 0) {
    size_t kl = strlen(key);
    int is_weight = kl > 7 && strcmp(key + kl - 7, ".weight") == 0 && mlx_array_ndim(val) == 2;
    if (!is_weight) {
      mlx_map_string_to_array_insert(q, key, val);
      continue;
    }
    mlx_vector_array parts = mlx_vector_array_new();
    mlx_optional_int gs = {C.quant_group, true}, bt = {bits, true};
    mlx_array nul = {0};
    CK(mlx_quantize(&parts, val, gs, bt, "affine", nul, cpu));
    char stem[512], k2[600];
    snprintf(stem, sizeof stem, "%.*s", (int)(kl - 7), key);
    const char* suffix[3] = {"weight", "scales", "biases"};
    for (int i = 0; i < 3; i++) {
      mlx_array a = mlx_array_new();
      mlx_vector_array_get(&a, parts, (size_t)i);
      snprintf(k2, sizeof k2, "%s.%s", stem, suffix[i]);
      mlx_map_string_to_array_insert(q, k2, a);
      mlx_array_free(a);
    }
    mlx_vector_array_free(parts);
  }
  mlx_array_free(val);
  mlx_map_string_to_array_iterator_free(it);
  mkdir(out, 0755);
  snprintf(p, sizeof p, "%s/model.safetensors", out);
  if (mlx_save_safetensors(p, q, meta) != 0) fail("TrainingFailed", "cannot write %s: %s", p, g_mlx_err);
  mlx_stream_free(cpu);
}

/* --selftest: the analytic LoRA gradient against central finite differences
 * on a small random Qwen2 model, dense and quantised, tied and untied, with
 * and without gradient checkpointing (which must not change the gradient),
 * with a frozen layer below the LoRA layers; and the chunked loss against
 * the unchunked one (the same loss and gradient). */
static void random_lin(Lin* l, int out, int in, int bias, mlx_array* key, float scale) {
  memset(l, 0, sizeof *l);
  int sh[2] = {out, in}, shb[1] = {out};
  mlx_array k1 = mlx_array_new(), k2 = mlx_array_new();
  CK(mlx_random_split(&k1, &k2, *key, S));
  mlx_array_set(key, k1);
  l->w = mlx_array_new();
  CK(mlx_random_normal(&l->w, sh, 2, MLX_FLOAT32, 0.0f, scale, k2, S));
  if (bias) {
    mlx_array k3 = mlx_array_new(), k4 = mlx_array_new();
    CK(mlx_random_split(&k3, &k4, *key, S));
    mlx_array_set(key, k3);
    l->b = mlx_array_new();
    CK(mlx_random_normal(&l->b, shb, 1, MLX_FLOAT32, 0.0f, scale, k4, S));
    mlx_array_free(k3);
    mlx_array_free(k4);
  }
  mlx_array_free(k1);
  mlx_array_free(k2);
  if (C.quant_bits) {
    mlx_vector_array outv = mlx_vector_array_new();
    mlx_optional_int gs = {C.quant_group, true}, bits = {C.quant_bits, true};
    mlx_array nul = {0};
    CK(mlx_quantize(&outv, l->w, gs, bits, "affine", nul, S));
    l->qw = mlx_array_new();
    l->qs = mlx_array_new();
    l->qb = mlx_array_new();
    mlx_vector_array_get(&l->qw, outv, 0);
    mlx_vector_array_get(&l->qs, outv, 1);
    mlx_vector_array_get(&l->qb, outv, 2);
    mlx_vector_array_free(outv);
    l->quant = 1;
    l->bits = C.quant_bits;
    l->group = C.quant_group;
  }
}

/* The loss and gradient of vg on in against a reference: each must agree to
 * tol (absolute). Returns the number of disagreements; *checked counts the
 * comparisons. */
static int grad_diff(mlx_array ids, mlx_array tg, mlx_array mk, const mlx_array* G, float ref_loss, float tol,
                     const char* what, int* checked) {
  int bad = 0;
  mlx_array* g2 = calloc((size_t)NP, sizeof *g2);
  float lv = micro_loss(P, ids, tg, mk, g2);
  eval_arrays(g2, NP);
  (*checked)++;
  if (fabsf(lv - ref_loss) > tol) {
    fprintf(stderr, "selftest: %s loss %.9g differs from %.9g\n", what, lv, ref_loss);
    bad++;
  }
  for (int i = 0; i < NP; i++) {
    mlx_array d = mlx_array_new(), ad = mlx_array_new(), mx = mlx_array_new();
    CK(mlx_subtract(&d, g2[i], G[i], S));
    CK(mlx_abs(&ad, d, S));
    CK(mlx_max(&mx, ad, false, S));
    float m = scalar(mx);
    if (m > tol) {
      fprintf(stderr, "selftest: %s gradient of %s differs by %g\n", what, PNAME[i], m);
      bad++;
    }
    (*checked)++;
    mlx_array_free(g2[i]);
    mlx_array_free(d);
    mlx_array_free(ad);
    mlx_array_free(mx);
  }
  free(g2);
  pool_free();
  return bad;
}

static int gradcheck_case(int tied, int ckpt, int qbits, int lora_layers) {
  C.layers = 2;
  C.hidden = 32;
  C.heads = 4;
  C.kv_heads = 2;
  C.head_dim = 8;
  C.inter = 64;
  C.vocab = 48;
  C.tied = tied;
  C.rank = 4;
  C.alpha = 8;
  C.lora_layers = lora_layers;
  C.targets = (1u << NTARGET) - 1;
  C.quant_bits = qbits;
  C.quant_group = 32;
  C.grad_ckpt = 0;
  C.loss_chunk = 0; /* the reference: the unchunked loss */
  mlx_array key = mlx_array_new();
  CK(mlx_random_key(&key, 1234));
  L = calloc((size_t)C.layers, sizeof *L);
  random_lin(&EMB, C.vocab, C.hidden, 0, &key, 0.5f);
  if (!tied) random_lin(&HEAD, C.vocab, C.hidden, 0, &key, 0.5f);
  OUT = tied ? &EMB : &HEAD;
  int shn[1] = {C.hidden};
  NORM = mlx_array_new();
  CK(mlx_ones(&NORM, shn, 1, MLX_FLOAT32, S));
  for (int li = 0; li < C.layers; li++) {
    for (int t = 0; t < NTARGET; t++)
      random_lin(&L[li].p[t], target_out(t), target_in(t), t == T_Q || t == T_K || t == T_V, &key, 0.2f);
    L[li].ln1 = mlx_array_new();
    L[li].ln2 = mlx_array_new();
    CK(mlx_ones(&L[li].ln1, shn, 1, MLX_FLOAT32, S));
    CK(mlx_ones(&L[li].ln2, shn, 1, MLX_FLOAT32, S));
  }
  params_alloc();
  params_init(99);
  /* Non-zero B, so every LoRA matrix has a gradient. */
  for (int i = 1; i < NP; i += 2) {
    const int* sh = mlx_array_shape(P[i]);
    mlx_array k1 = mlx_array_new(), k2 = mlx_array_new();
    CK(mlx_random_split(&k1, &k2, key, S));
    mlx_array_set(&key, k1);
    CK(mlx_random_normal(&P[i], sh, 2, MLX_FLOAT32, 0.0f, 0.2f, k2, S));
    mlx_array_free(k1);
    mlx_array_free(k2);
  }
  eval_arrays(P, NP);
  /* Two rows of 9 tokens, prompts of 3 and 1. */
  static uint32_t toks[18], lens[2] = {9, 9}, prompts[2] = {3, 1};
  uint64_t off[2] = {0, 9};
  for (int i = 0; i < 18; i++) toks[i] = (uint32_t)((i * 7 + 3) % C.vocab);
  Tok t = {.rows = 2, .vocab = (uint32_t)C.vocab, .ntok = 18, .lens = lens, .prompts = prompts, .tok = toks, .off = off};
  uint32_t rows[2] = {0, 1};
  mlx_array ids, tg, mk;
  make_batch(&t, rows, 2, &ids, &tg, &mk, NULL);
  mlx_closure cls = mlx_closure_new_func_payload(loss_closure, (void*)1, NULL);
  int* argn = malloc((size_t)NP * sizeof *argn);
  for (int i = 0; i < NP; i++) argn[i] = i;
  mlx_closure_value_and_grad vg = mlx_closure_value_and_grad_new();
  CK(mlx_value_and_grad(&vg, cls, argn, (size_t)NP));
  g_loss_p = cls;
  g_vg = vg;
  mlx_vector_array in = inputs(P, ids, tg, mk), vals = mlx_vector_array_new(), grads = mlx_vector_array_new();
  CK(mlx_closure_value_and_grad_apply(&vals, &grads, vg, in));
  mlx_array* G = calloc((size_t)NP, sizeof *G);
  for (int i = 0; i < NP; i++) {
    G[i] = mlx_array_new();
    mlx_vector_array_get(&G[i], grads, (size_t)i);
  }
  eval_arrays(G, NP);
  pool_free();
  int bad = 0, checked = 0;
  mlx_array l0 = mlx_array_new();
  mlx_vector_array_get(&l0, vals, 0);
  CK(mlx_array_eval(l0));
  float ref_loss = 0;
  CK(mlx_array_item_float32(&ref_loss, l0));
  mlx_array_free(l0);
  /* The chunked loss: the same loss and gradient as the unchunked one, with
     chunks that divide the 16 target tokens unevenly (5) and one token a
     chunk (1). */
  int chunks[2] = {5, 1};
  for (int c = 0; c < 2; c++) {
    char what[64];
    snprintf(what, sizeof what, "loss chunk %d:", chunks[c]);
    C.loss_chunk = chunks[c];
    bad += grad_diff(ids, tg, mk, G, ref_loss, 1e-5f, what, &checked);
  }
  if (ckpt) {
    /* The same gradient through the checkpointed layers (the unchunked loss:
       the chunked loss recomputes each layer in its own backward pass). */
    C.grad_ckpt = 1;
    checkpoint_closures();
    C.loss_chunk = 0;
    bad += grad_diff(ids, tg, mk, G, ref_loss, 1e-5f, "checkpointed:", &checked);
  }
  /* The finite differences check the chunked loss, the trainer's default. */
  C.loss_chunk = 5;
  const float eps = 1e-2f;
  for (int i = 0; i < NP; i++) {
    mlx_array gc = mlx_array_new(), pc = mlx_array_new();
    CK(mlx_contiguous(&gc, G[i], false, S));
    CK(mlx_contiguous(&pc, P[i], false, S));
    CK(mlx_array_eval(gc));
    CK(mlx_array_eval(pc));
    size_t n = mlx_array_size(pc);
    const float* gd = mlx_array_data_float32(gc);
    float* host = malloc(n * 4);
    memcpy(host, mlx_array_data_float32(pc), n * 4);
    const int* sh = mlx_array_shape(pc);
    int shc[2] = {sh[0], sh[1]};
    for (int k = 0; k < 3; k++) {
      size_t e = (size_t)((k * 2654435761u + (unsigned)i * 40503u) % n);
      float keep = host[e];
      mlx_array* prm = calloc((size_t)NP, sizeof *prm);
      for (int j = 0; j < NP; j++) prm[j] = P[j];
      host[e] = keep + eps;
      mlx_array plus = mlx_array_new_data(host, shc, 2, MLX_FLOAT32);
      prm[i] = plus;
      float lp = micro_loss(prm, ids, tg, mk, NULL);
      host[e] = keep - eps;
      mlx_array minus = mlx_array_new_data(host, shc, 2, MLX_FLOAT32);
      prm[i] = minus;
      float lm = micro_loss(prm, ids, tg, mk, NULL);
      host[e] = keep;
      pool_free();
      mlx_array_free(plus);
      mlx_array_free(minus);
      free(prm);
      double fd = ((double)lp - (double)lm) / (2.0 * eps), an = gd[e];
      double tol = 2e-3 + 5e-2 * fmax(fabs(fd), fabs(an));
      checked++;
      if (fabs(fd - an) > tol) {
        fprintf(stderr, "selftest: %s[%zu]: analytic %.6g, finite difference %.6g\n", PNAME[i], e, an, fd);
        bad++;
      }
    }
    free(host);
    mlx_array_free(gc);
    mlx_array_free(pc);
  }
  fprintf(stderr, "selftest: tied=%d checkpoint=%d bits=%d lora layers=%d: %d checks, %d bad\n", tied, ckpt, qbits,
          lora_layers, checked, bad);
  mlx_vector_array_free(in);
  mlx_vector_array_free(vals);
  mlx_vector_array_free(grads);
  return bad;
}

static int selftest(void) {
  int bad = 0;
  bad += gradcheck_case(0, 0, 0, 2);
  bad += gradcheck_case(1, 1, 0, 2);
  bad += gradcheck_case(1, 0, 4, 2);
  bad += gradcheck_case(0, 1, 8, 2);
  /* A frozen layer below the LoRA layer: the chunked loss runs it forward only. */
  bad += gradcheck_case(1, 1, 4, 1);
  printf("selftest %s\n", bad ? "FAILED" : "ok");
  return bad ? 1 : 0;
}
