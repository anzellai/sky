// sky_llama_jni.cpp - the Android JNI face of sky_llama.h, for the shell's
// sky.ai.SkyLlama class (rust/crates/sky/src/main.rs, ANDROID_AI_JAVA).
#include <jni.h>
#include <cstring>
#include <string>
#include "sky_llama.h"

static std::string str(JNIEnv * env, jstring s) {
    if (!s) return std::string();
    const char * c = env->GetStringUTFChars(s, nullptr);
    std::string out(c ? c : "");
    if (c) env->ReleaseStringUTFChars(s, c);
    return out;
}

extern "C" {

JNIEXPORT jint JNICALL Java_sky_ai_SkyLlama_init(JNIEnv *, jclass) {
    return sky_llama_init(0);
}

JNIEXPORT jint JNICALL Java_sky_ai_SkyLlama_load(JNIEnv * env, jclass, jstring path, jint nCtx, jint threads) {
    return sky_llama_load(str(env, path).c_str(), 0, nCtx, threads);
}

JNIEXPORT jint JNICALL Java_sky_ai_SkyLlama_adapter(JNIEnv * env, jclass, jstring path, jfloat scale) {
    return sky_llama_adapter(str(env, path).c_str(), scale);
}

// The answer as UTF-8 bytes: JNI's NewStringUTF reads modified UTF-8, which
// garbles a character outside the Basic Multilingual Plane.
JNIEXPORT jbyteArray JNICALL Java_sky_ai_SkyLlama_chat(JNIEnv * env, jclass, jbyteArray request) {
    jsize n = env->GetArrayLength(request);
    std::string req((size_t) n, '\0');
    env->GetByteArrayRegion(request, 0, n, reinterpret_cast<jbyte *>(&req[0]));
    const char * out = sky_llama_chat(req.c_str());
    jsize m = (jsize) strlen(out);
    jbyteArray arr = env->NewByteArray(m);
    env->SetByteArrayRegion(arr, 0, m, reinterpret_cast<const jbyte *>(out));
    return arr;
}

JNIEXPORT jint JNICALL Java_sky_ai_SkyLlama_tokenize(JNIEnv * env, jclass, jstring text) {
    return sky_llama_tokenize(str(env, text).c_str());
}

JNIEXPORT void JNICALL Java_sky_ai_SkyLlama_unload(JNIEnv *, jclass) {
    sky_llama_unload();
}

JNIEXPORT jstring JNICALL Java_sky_ai_SkyLlama_lastError(JNIEnv * env, jclass) {
    return env->NewStringUTF(sky_llama_last_error());
}

}
