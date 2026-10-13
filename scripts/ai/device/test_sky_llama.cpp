// test_sky_llama.cpp - the engine core's contract, on the build machine's CPU.
//
//   sky_llama_test <model.gguf>
//
// With greedy sampling the streamed answer (sky_llama_chat_begin, then
// sky_llama_chat_next until done) is the one-call answer (sky_llama_chat):
// the deltas concatenate to its content, the final body equals it, and every
// delta is whole UTF-8. A next with no answer in progress is an error object,
// and a begin while an answer is in progress starts a new one. Exit 0 PASS,
// 1 FAIL. Run by test-engine-core.sh and the ai-device-bundles workflow.
#include "sky_llama.h"

#include <nlohmann/json.hpp>

#include <cstdio>
#include <string>

using json = nlohmann::json;

static int failures = 0;

static void check(bool ok, const char * what, const std::string & detail) {
    std::printf("%s %s: %s\n", ok ? "ok  " : "FAIL", what, detail.c_str());
    if (!ok) {
        failures++;
    }
}

static bool whole_utf8(const std::string & s) {
    size_t i = 0;
    while (i < s.size()) {
        unsigned char c = static_cast<unsigned char>(s[i]);
        size_t n = c < 0x80 ? 1 : (c >> 5) == 0x6 ? 2 : (c >> 4) == 0xE ? 3 : (c >> 3) == 0x1E ? 4 : 0;
        if (n == 0 || i + n > s.size()) {
            return false;
        }
        for (size_t k = 1; k < n; k++) {
            if ((static_cast<unsigned char>(s[i + k]) & 0xC0) != 0x80) {
                return false;
            }
        }
        i += n;
    }
    return true;
}

int main(int argc, char ** argv) {
    if (argc < 2) {
        std::fprintf(stderr, "usage: sky_llama_test <model.gguf>\n");
        return 2;
    }
    sky_llama_init(0);
    if (sky_llama_load(argv[1], 0, 512, 2) != 0) {
        std::fprintf(stderr, "load: %s\n", sky_llama_last_error());
        return 2;
    }
    const std::string req =
        R"({"messages":[{"role":"user","content":"Say hello to the world."}],"max_tokens":24,"temperature":0})";

    json no = json::parse(sky_llama_chat_next(), nullptr, false);
    check(no.contains("error"), "next with no answer in progress is an error", no.dump());

    json whole = json::parse(sky_llama_chat(req.c_str()), nullptr, false);
    std::string want = whole["choices"][0]["message"]["content"].get<std::string>();
    check(!whole.contains("error") && whole["usage"]["completion_tokens"].get<int>() > 0,
          "the one-call answer", want);

    // A begin while an answer is in progress starts again.
    check(std::string(sky_llama_chat_begin(req.c_str())).empty(), "begin", "");
    json first = json::parse(sky_llama_chat_next(), nullptr, false);
    check(first.contains("delta") || first.contains("done"), "a first event", first.dump());
    check(std::string(sky_llama_chat_begin(req.c_str())).empty(), "begin again", "");

    std::string got;
    int deltas = 0;
    bool utf8 = true;
    json body;
    for (int i = 0; i < 1000; i++) {
        json ev = json::parse(sky_llama_chat_next(), nullptr, false);
        if (ev.contains("delta")) {
            std::string d = ev["delta"].get<std::string>();
            utf8 = utf8 && !d.empty() && whole_utf8(d);
            got += d;
            deltas++;
            continue;
        }
        if (ev.contains("done")) {
            body = ev["body"];
        }
        break;
    }
    check(deltas > 1, "the answer arrived in pieces", std::to_string(deltas) + " deltas");
    check(utf8, "every delta is whole UTF-8", "");
    check(got == want, "the deltas are the one-call answer", got);
    check(body.is_object() && body["choices"][0]["message"]["content"] == want &&
              body["usage"]["completion_tokens"] == whole["usage"]["completion_tokens"],
          "the final body is the one-call body", body.dump().substr(0, 200));
    json after = json::parse(sky_llama_chat_next(), nullptr, false);
    check(after.contains("error"), "the answer ended", after.dump());

    sky_llama_unload();
    std::printf("%s (%d failed)\n", failures == 0 ? "PASS" : "FAIL", failures);
    return failures == 0 ? 0 : 1;
}
