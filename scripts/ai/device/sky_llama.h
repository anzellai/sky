/* sky_llama.h - the one C API every Std.Ai.Device backend runs.
 *
 * The browser engine (Emscripten, WebGPU and CPU builds), the iOS
 * xcframework and the Android JNI library all compile sky_llama.cpp over the
 * same pinned llama.cpp, so a device answers with the same prompt template,
 * the same sampler and the same OpenAI-shaped JSON on every target. One model
 * (and at most one LoRA adapter) is loaded at a time.
 *
 * Every string a function returns is owned by the library and stays valid
 * until the next call. Calls are not reentrant: the caller serialises them.
 */
#ifndef SKY_LLAMA_H
#define SKY_LLAMA_H

#ifdef __cplusplus
extern "C" {
#endif

/* Initialise the backends. Returns the number of devices ggml registered. */
int sky_llama_init(int verbose);

/* The devices, as "name:description;" pairs. */
const char * sky_llama_devices(void);

/* The number of devices that are not the CPU (a GPU through WebGPU or Metal). */
int sky_llama_gpu_devices(void);

/* Load a GGUF model. n_gpu_layers 0 runs on the CPU, -1 offloads every
 * layer. Returns 0, or a negative code with sky_llama_last_error set. */
int sky_llama_load(const char * path, int n_gpu_layers, int n_ctx, int n_threads);

/* Apply a GGUF LoRA adapter at a scale; an empty path removes it.
 * Returns 0, or a negative code with sky_llama_last_error set. */
int sky_llama_adapter(const char * path, float scale);

/* Answer one OpenAI chat-completions request body (messages, max_tokens,
 * temperature, seed) with an OpenAI chat-completions response body, or with
 * {"error":{"code":...,"message":...}}. */
const char * sky_llama_chat(const char * request_json);

/* Stream one answer. sky_llama_chat_begin decodes the request's prompt and
 * returns "" (or an error object as sky_llama_chat does). Each
 * sky_llama_chat_next then returns {"delta":"<text>"} for the next whole UTF-8
 * text the model wrote, and finally {"done":true,"body":<the same response
 * sky_llama_chat returns>}. A new begin, a load or an unload ends an answer in
 * progress. */
const char * sky_llama_chat_begin(const char * request_json);
const char * sky_llama_chat_next(void);

/* The number of tokens a text has with the model's tokenizer, or -1. */
int sky_llama_tokenize(const char * text);

/* Free the model, the adapter and the context. */
void sky_llama_unload(void);

/* The last failure's text. */
const char * sky_llama_last_error(void);

#ifdef __cplusplus
}
#endif

#endif
