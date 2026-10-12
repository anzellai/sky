# The Std.Ai.Device engine: sky_llama.cpp over the pinned llama.cpp.
# Driven by build-device-bundles.sh; SKY_TARGET is web, android or ios.
cmake_minimum_required(VERSION 3.20)
project(skyllama C CXX)

set(LLAMA_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_TOOLS OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_SERVER OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_COMMON OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_HTML OFF CACHE BOOL "" FORCE)
set(LLAMA_OPENSSL OFF CACHE BOOL "" FORCE)
set(LLAMA_CURL OFF CACHE BOOL "" FORCE)
set(GGML_OPENMP OFF CACHE BOOL "" FORCE)
set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
add_subdirectory(${LLAMA_SRC} llama)

set(SKY_SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/sky_llama.cpp)
set(SKY_INCLUDES ${CMAKE_CURRENT_SOURCE_DIR} ${LLAMA_SRC}/vendor)

if(SKY_TARGET STREQUAL "web")
  add_executable(skyllama ${SKY_SOURCES})
  set_target_properties(skyllama PROPERTIES SUFFIX ".mjs")
  target_include_directories(skyllama PRIVATE ${SKY_INCLUDES})
  target_link_libraries(skyllama PRIVATE llama)
  target_link_options(skyllama PRIVATE
    -sMODULARIZE=1 -sEXPORT_ES6=1 -sEXPORT_NAME=createSkyLlama
    -sENVIRONMENT=worker
    -sDYNAMIC_EXECUTION=0
    -sALLOW_MEMORY_GROWTH=1
    -sMAXIMUM_MEMORY=4GB
    -sSTACK_SIZE=1048576
    -lworkerfs.js
    -sEXPORTED_RUNTIME_METHODS=ccall,FS
    -sEXPORTED_FUNCTIONS=_malloc,_free,_sky_llama_init,_sky_llama_devices,_sky_llama_gpu_devices,_sky_llama_load,_sky_llama_adapter,_sky_llama_chat,_sky_llama_tokenize,_sky_llama_unload,_sky_llama_last_error
    ${SKY_EXTRA_LINK})
elseif(SKY_TARGET STREQUAL "android")
  add_library(skyllama SHARED ${SKY_SOURCES} ${CMAKE_CURRENT_SOURCE_DIR}/sky_llama_jni.cpp)
  target_include_directories(skyllama PRIVATE ${SKY_INCLUDES})
  target_link_libraries(skyllama PRIVATE llama log android)
  # 16 KB pages (Android 15 devices), unused code dropped, the C++ runtime
  # linked in so the library has no libc++_shared.so dependency.
  target_link_options(skyllama PRIVATE -Wl,-z,max-page-size=16384 -Wl,--gc-sections -static-libstdc++)
elseif(SKY_TARGET STREQUAL "ios")
  add_library(skyllama_core STATIC ${SKY_SOURCES})
  target_include_directories(skyllama_core PRIVATE ${SKY_INCLUDES})
  target_link_libraries(skyllama_core PRIVATE llama)
else()
  message(FATAL_ERROR "SKY_TARGET must be web, android or ios")
endif()
