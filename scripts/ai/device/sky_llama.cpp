// sky_llama.cpp - the Std.Ai.Device engine core over the pinned llama.cpp.
//
// Built three ways by scripts/ai/device/build-device-bundles.sh (and by the
// ai-device-bundles workflow): with Emscripten for the browser worker, as the
// static library inside SkyLlama.xcframework for iOS, and as the Android JNI
// library. See sky_llama.h for the contract.

#include "sky_llama.h"
#include "llama.h"
#include "ggml-backend.h"

#include <nlohmann/json.hpp>

#include <chrono>
#include <cstdio>
#include <cstring>
#include <exception>
#include <string>
#include <vector>

#ifdef __EMSCRIPTEN__
#include <emscripten/emscripten.h>
#define SKY_EXPORT EMSCRIPTEN_KEEPALIVE
#else
#define SKY_EXPORT __attribute__((visibility("default")))
#endif

using json = nlohmann::json;

namespace {

llama_model * g_model = nullptr;
llama_context * g_ctx = nullptr;
llama_adapter_lora * g_lora = nullptr;
int g_n_ctx = 0;
bool g_inited = false;
std::string g_out;
std::string g_err;

double now_ms() {
    return std::chrono::duration<double, std::milli>(
               std::chrono::steady_clock::now().time_since_epoch())
        .count();
}

void quiet_log(ggml_log_level level, const char * text, void *) {
    if (level >= GGML_LOG_LEVEL_ERROR) {
        fputs(text, stderr);
    }
}

std::string error_json(const std::string & code, const std::string & message) {
    json j = {{"error", {{"code", code}, {"message", message}}}};
    return j.dump(-1, ' ', false, json::error_handler_t::replace);
}

void free_lora() {
    if (g_ctx) {
        llama_set_adapters_lora(g_ctx, nullptr, 0, nullptr);
    }
    if (g_lora) {
        llama_adapter_lora_free(g_lora);
        g_lora = nullptr;
    }
}

// The prompt: the model's own chat template over every message.
bool apply_template(const json & messages, std::string & out) {
    std::vector<std::string> roles, contents;
    for (const auto & m : messages) {
        roles.push_back(m.value("role", std::string("user")));
        const auto & c = m.contains("content") ? m["content"] : json();
        contents.push_back(c.is_string() ? c.get<std::string>() : std::string());
    }
    std::vector<llama_chat_message> chat;
    for (size_t i = 0; i < roles.size(); i++) {
        chat.push_back({roles[i].c_str(), contents[i].c_str()});
    }
    const char * tmpl = llama_model_chat_template(g_model, nullptr);
    size_t total = 0;
    for (auto & c : contents) {
        total += c.size();
    }
    std::vector<char> buf(2 * total + 1024);
    int n = llama_chat_apply_template(tmpl, chat.data(), chat.size(), true, buf.data(), (int) buf.size());
    if (n > (int) buf.size()) {
        buf.resize(n + 1);
        n = llama_chat_apply_template(tmpl, chat.data(), chat.size(), true, buf.data(), (int) buf.size());
    }
    if (n < 0) {
        return false;
    }
    out.assign(buf.data(), n);
    return true;
}

std::string chat_impl(const char * request_json) {
    if (!g_model || !g_ctx) {
        return error_json("not_loaded", "no model is loaded");
    }
    json req = json::parse(request_json, nullptr, false);
    if (req.is_discarded() || !req.is_object() || !req.contains("messages") || !req["messages"].is_array()) {
        return error_json("invalid_request", "the request has no messages");
    }
    std::string prompt;
    if (!apply_template(req["messages"], prompt)) {
        return error_json("template", "the model's chat template cannot format these messages");
    }
    const llama_vocab * vocab = llama_model_get_vocab(g_model);
    int n_prompt = -llama_tokenize(vocab, prompt.c_str(), (int) prompt.size(), nullptr, 0, true, true);
    std::vector<llama_token> toks(n_prompt > 0 ? n_prompt : 0);
    if (n_prompt <= 0 || llama_tokenize(vocab, prompt.c_str(), (int) prompt.size(), toks.data(), n_prompt, true, true) < 0) {
        return error_json("tokenize", "the prompt cannot be tokenized");
    }
    int max_tokens = 256;
    if (req.contains("max_tokens") && req["max_tokens"].is_number_integer()) {
        max_tokens = req["max_tokens"].get<int>();
    }
    if (max_tokens <= 0) {
        max_tokens = 256;
    }
    if (n_prompt >= g_n_ctx) {
        return error_json("context", "a prompt of " + std::to_string(n_prompt) +
                                         " tokens does not fit the device context of " + std::to_string(g_n_ctx) + " tokens");
    }
    if (n_prompt + max_tokens > g_n_ctx) {
        max_tokens = g_n_ctx - n_prompt;
    }
    float temp = 0.8f;
    bool has_temp = req.contains("temperature") && req["temperature"].is_number();
    if (has_temp) {
        temp = req["temperature"].get<float>();
    }
    uint32_t seed = LLAMA_DEFAULT_SEED;
    if (req.contains("seed") && req["seed"].is_number_integer()) {
        seed = (uint32_t) req["seed"].get<long long>();
    }

    llama_memory_clear(llama_get_memory(g_ctx), true);
    llama_sampler * smpl = llama_sampler_chain_init(llama_sampler_chain_default_params());
    if (has_temp && temp <= 0.0f) {
        llama_sampler_chain_add(smpl, llama_sampler_init_greedy());
    } else {
        llama_sampler_chain_add(smpl, llama_sampler_init_top_k(40));
        llama_sampler_chain_add(smpl, llama_sampler_init_top_p(0.95f, 1));
        llama_sampler_chain_add(smpl, llama_sampler_init_temp(temp));
        llama_sampler_chain_add(smpl, llama_sampler_init_dist(seed));
    }

    double t0 = now_ms();
    const int n_batch = (int) llama_n_batch(g_ctx);
    for (int i = 0; i < n_prompt; i += n_batch) {
        int n = std::min(n_batch, n_prompt - i);
        if (llama_decode(g_ctx, llama_batch_get_one(toks.data() + i, n)) != 0) {
            llama_sampler_free(smpl);
            return error_json("decode", "the prompt could not be decoded");
        }
    }
    double t1 = now_ms();
    std::string content;
    int gen = 0;
    std::string finish = "length";
    for (; gen < max_tokens; gen++) {
        llama_token tok = llama_sampler_sample(smpl, g_ctx, -1);
        if (llama_vocab_is_eog(vocab, tok)) {
            finish = "stop";
            break;
        }
        char piece[256];
        int k = llama_token_to_piece(vocab, tok, piece, sizeof piece, 0, false);
        if (k > 0) {
            content.append(piece, k);
        }
        if (llama_decode(g_ctx, llama_batch_get_one(&tok, 1)) != 0) {
            break;
        }
    }
    double t2 = now_ms();
    llama_sampler_free(smpl);

    json resp = {
        {"id", "sky-device"},
        {"object", "chat.completion"},
        {"model", "device"},
        {"choices", json::array({{{"index", 0},
                                  {"message", {{"role", "assistant"}, {"content", content}}},
                                  {"finish_reason", finish}}})},
        {"usage", {{"prompt_tokens", n_prompt}, {"completion_tokens", gen}, {"total_tokens", n_prompt + gen}}},
        {"timings",
         {{"prompt_ms", t1 - t0},
          {"predicted_ms", t2 - t1},
          {"predicted_per_second", gen > 0 && t2 > t1 ? gen / ((t2 - t1) / 1000.0) : 0.0}}},
    };
    return resp.dump(-1, ' ', false, json::error_handler_t::replace);
}

} // namespace

extern "C" {

SKY_EXPORT int sky_llama_init(int verbose) {
    if (!g_inited) {
        if (!verbose) {
            llama_log_set(quiet_log, nullptr);
        }
        llama_backend_init();
        g_inited = true;
    }
    return (int) ggml_backend_dev_count();
}

SKY_EXPORT const char * sky_llama_devices(void) {
    g_out.clear();
    for (size_t i = 0; i < ggml_backend_dev_count(); i++) {
        auto * d = ggml_backend_dev_get(i);
        g_out += ggml_backend_dev_name(d);
        g_out += ":";
        g_out += ggml_backend_dev_description(d);
        g_out += ";";
    }
    return g_out.c_str();
}

SKY_EXPORT int sky_llama_gpu_devices(void) {
    int n = 0;
    for (size_t i = 0; i < ggml_backend_dev_count(); i++) {
        auto t = ggml_backend_dev_type(ggml_backend_dev_get(i));
        if (t == GGML_BACKEND_DEVICE_TYPE_GPU || t == GGML_BACKEND_DEVICE_TYPE_IGPU) {
            n++;
        }
    }
    return n;
}

SKY_EXPORT int sky_llama_load(const char * path, int n_gpu_layers, int n_ctx, int n_threads) {
    try {
        sky_llama_unload();
        auto mp = llama_model_default_params();
        mp.n_gpu_layers = n_gpu_layers;
#ifdef __EMSCRIPTEN__
        // The worker mounts the stored file on WORKERFS: read it, never map it.
        mp.load_mode = LLAMA_LOAD_MODE_NONE;
#endif
        g_model = llama_model_load_from_file(path, mp);
        if (!g_model) {
            g_err = std::string("the model at ") + path + " could not be loaded";
            return -1;
        }
        auto cp = llama_context_default_params();
        cp.n_ctx = n_ctx > 0 ? n_ctx : 2048;
        cp.n_batch = 512;
        cp.n_threads = n_threads > 0 ? n_threads : 1;
        cp.n_threads_batch = cp.n_threads;
        g_ctx = llama_init_from_model(g_model, cp);
        if (!g_ctx) {
            g_err = "the model's context could not be created";
            llama_model_free(g_model);
            g_model = nullptr;
            return -2;
        }
        g_n_ctx = (int) llama_n_ctx(g_ctx);
        return 0;
    } catch (const std::exception & e) {
        g_err = e.what();
        return -9;
    }
}

SKY_EXPORT int sky_llama_adapter(const char * path, float scale) {
    try {
        if (!g_model || !g_ctx) {
            g_err = "no model is loaded";
            return -1;
        }
        free_lora();
        if (!path || !*path) {
            return 0;
        }
        g_lora = llama_adapter_lora_init(g_model, path);
        if (!g_lora) {
            g_err = std::string("the adapter at ") + path + " could not be loaded for this model";
            return -2;
        }
        float s = scale;
        if (llama_set_adapters_lora(g_ctx, &g_lora, 1, &s) != 0) {
            g_err = "the adapter could not be applied";
            return -3;
        }
        return 0;
    } catch (const std::exception & e) {
        g_err = e.what();
        return -9;
    }
}

SKY_EXPORT const char * sky_llama_chat(const char * request_json) {
    try {
        g_out = chat_impl(request_json);
    } catch (const std::exception & e) {
        g_out = error_json("exception", e.what());
    }
    return g_out.c_str();
}

SKY_EXPORT int sky_llama_tokenize(const char * text) {
    if (!g_model) {
        return -1;
    }
    const llama_vocab * vocab = llama_model_get_vocab(g_model);
    return -llama_tokenize(vocab, text, (int) strlen(text), nullptr, 0, true, true);
}

SKY_EXPORT void sky_llama_unload(void) {
    free_lora();
    if (g_ctx) {
        llama_free(g_ctx);
        g_ctx = nullptr;
    }
    if (g_model) {
        llama_model_free(g_model);
        g_model = nullptr;
    }
    g_n_ctx = 0;
}

SKY_EXPORT const char * sky_llama_last_error(void) {
    return g_err.c_str();
}

} // extern "C"
