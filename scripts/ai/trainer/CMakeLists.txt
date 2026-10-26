# scripts/ai/trainer/CMakeLists.txt - sky-trainer, the native LoRA trainer of
# Std.Ai.Train, and its mlx-c library, for the release bundle. CI runs it
# through build-trainer-bundle.sh (.github/workflows/ai-trainer-bundles.yml);
# build.sh is the contributor bootstrap on a Mac and compiles the same sources
# with the same flags.
#
#   cmake -S scripts/ai/trainer -B build -DCMAKE_BUILD_TYPE=Release \
#         -DMLX_PREFIX=<an MLX 0.32.4 install> -DMLXC_SRC=<mlx-c at 57731b00e>
#   cmake --build build && cmake --install build --prefix <bundle>
#
# The install is the bundle's layout: sky-trainer at the top, libmlxc in lib/
# beside libmlx. sky-trainer finds lib/ by rpath, libmlxc finds libmlx beside
# itself.
cmake_minimum_required(VERSION 3.25)
project(sky_trainer C CXX)

set(SKY_MLX_PIN "0.32.4")
set(MLX_PREFIX "" CACHE PATH "an MLX ${SKY_MLX_PIN} install (include/, lib/)")
set(MLXC_SRC "" CACHE PATH "an mlx-c source tree at 57731b00e")
if(NOT EXISTS "${MLX_PREFIX}/include/mlx/version.h")
  message(FATAL_ERROR "MLX_PREFIX must be an MLX ${SKY_MLX_PIN} install")
endif()
file(STRINGS "${MLX_PREFIX}/include/mlx/version.h" _mlx_v REGEX "^#define MLX_VERSION_(MAJOR|MINOR|PATCH) ")
string(REGEX MATCHALL "[0-9]+" _mlx_parts "${_mlx_v}")
list(JOIN _mlx_parts "." _mlx_version)
if(NOT _mlx_version STREQUAL SKY_MLX_PIN)
  message(FATAL_ERROR "MLX_PREFIX is MLX ${_mlx_version}; the trainer is pinned to ${SKY_MLX_PIN}")
endif()
if(NOT EXISTS "${MLXC_SRC}/mlx/c/mlx.h")
  message(FATAL_ERROR "MLXC_SRC must be an mlx-c source tree")
endif()

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

file(GLOB MLXC_SOURCES "${MLXC_SRC}/mlx/c/*.cpp")
add_library(mlxc SHARED ${MLXC_SOURCES})
target_include_directories(mlxc PUBLIC "${MLXC_SRC}" PRIVATE "${MLX_PREFIX}/include")
target_compile_definitions(mlxc PRIVATE NDEBUG)
find_library(MLX_LIB mlx PATHS "${MLX_PREFIX}/lib" NO_DEFAULT_PATH REQUIRED)
target_link_libraries(mlxc PRIVATE "${MLX_LIB}")

add_executable(sky-trainer sky_trainer.c guard.c sha256.c)
target_compile_options(sky-trainer PRIVATE -Wall -Wextra -Wno-unused-parameter)
target_compile_definitions(sky-trainer PRIVATE SKY_MLX_VERSION="${SKY_MLX_PIN}")
target_link_libraries(sky-trainer PRIVATE mlxc "${MLX_LIB}" m)

if(APPLE)
  set_target_properties(mlxc PROPERTIES INSTALL_NAME_DIR "@rpath"
    BUILD_RPATH "@loader_path" INSTALL_RPATH "@loader_path")
  set_target_properties(sky-trainer PROPERTIES
    BUILD_RPATH "@executable_path/lib" INSTALL_RPATH "@executable_path/lib")
else()
  set_target_properties(mlxc PROPERTIES BUILD_RPATH "$ORIGIN" INSTALL_RPATH "$ORIGIN")
  set_target_properties(sky-trainer PROPERTIES BUILD_RPATH "$ORIGIN/lib" INSTALL_RPATH "$ORIGIN/lib")
endif()

install(TARGETS sky-trainer RUNTIME DESTINATION .)
install(TARGETS mlxc LIBRARY DESTINATION lib)
