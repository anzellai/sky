# syntax=docker/dockerfile:1.7
#
# The Sky trainer image: the pinned CPython and uv, the hash-locked Unsloth
# toolchain (CUDA 13), and the Sky training driver with its cloud shim
# (driver/sky_cloud.py) as the entry point. A Vertex AI custom job runs it.
#
# The image is built in the user's own Google Cloud project with Cloud Build
# and is never published publicly. The build context is
# runtime-go/rt/ai_train_assets/ (so the paths below are driver/ and locks/).
# ADD --checksum needs BuildKit, so the build runs `docker buildx build`
# through a small Cloud Build config:
#
#   cat > /tmp/sky-trainer.cloudbuild.yaml <<'YAML'
#   steps:
#   - name: gcr.io/cloud-builders/docker
#     env: ["DOCKER_BUILDKIT=1"]
#     args: ["build", "-f", "image/trainer.dockerfile", "-t", "$_IMAGE", "."]
#   images: ["$_IMAGE"]
#   YAML
#   gcloud builds submit runtime-go/rt/ai_train_assets \
#     --config /tmp/sky-trainer.cloudbuild.yaml \
#     --substitutions _IMAGE=<region>-docker.pkg.dev/<project>/<repo>/sky-ai-trainer
#
# The build prints the pushed image's digest (sha256:...). Pin the job to
# that digest (<region>-docker.pkg.dev/<project>/<repo>/sky-ai-trainer@sha256:...),
# never to a tag. Every input below is pinned too: the base image by digest,
# CPython and uv by SHA-256 (from runtime-go/rt/ai_pins.go, linux amd64), and
# every Python package by the hashes in the lock. A changed archive or wheel
# fails the build.
#
# Base: nvidia/cuda:13.0.3-base-ubuntu24.04, the linux/amd64 manifest
# (index digest sha256:7c7413a56200486f71f181cad9310f6fd31b6bb21816ade15fc9c1e1e927a5c1).
# The base image carries the CUDA forward-compatibility libraries under
# /usr/local/cuda/compat, which the shim puts on LD_LIBRARY_PATH when the
# host's NVIDIA driver is older than 580.

ARG CUDA_BASE=nvidia/cuda:13.0.3-base-ubuntu24.04@sha256:97d085a7423ee18ec483a2878b9be2c976dc4ba908aef96518beb00e1899dcc4

# ── Build stage: the interpreter and the virtual environment ────────────
FROM --platform=linux/amd64 ${CUDA_BASE} AS build

# aiPythonTag 20260924, aiPythonVersion 3.12.14 (install_only_stripped).
ADD --checksum=sha256:269b2c99e4db15b242bf01832f4fea1e8f1a664f273cff519393f296e9820b41 \
    https://github.com/astral-sh/python-build-standalone/releases/download/20260924/cpython-3.12.14%2B20260924-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz \
    /tmp/cpython.tar.gz
# aiUvVersion 0.12.16.
ADD --checksum=sha256:8e5c6e5523dffc2dcf615bd995554c84c9feb4e577808a3fb8698a639d3f8d9c \
    https://github.com/astral-sh/uv/releases/download/0.12.16/uv-x86_64-unknown-linux-gnu.tar.gz \
    /tmp/uv.tar.gz

COPY locks/unsloth-linux-x86_64-cuda13.txt /tmp/unsloth.lock

ENV UV_NO_CONFIG=1 \
    UV_NO_CACHE=1 \
    UV_PYTHON_DOWNLOADS=never \
    PYTHONDONTWRITEBYTECODE=1

RUN set -eu; \
    mkdir -p /opt/sky /tmp/uv; \
    tar -xzf /tmp/cpython.tar.gz -C /opt/sky; \
    mv /opt/sky/python /opt/sky/cpython; \
    tar -xzf /tmp/uv.tar.gz -C /tmp/uv --strip-components=1; \
    /tmp/uv/uv venv --python /opt/sky/cpython/bin/python3.12 /opt/sky/venv; \
    /tmp/uv/uv pip sync --python /opt/sky/venv/bin/python \
        --require-hashes --only-binary :all: /tmp/unsloth.lock; \
    rm -rf /tmp/uv /tmp/uv.tar.gz /tmp/cpython.tar.gz /tmp/unsloth.lock /root/.cache

# ── Runtime stage ────────────────────────────────────────────────────────
FROM --platform=linux/amd64 ${CUDA_BASE}

COPY --from=build /opt/sky/cpython /opt/sky/cpython
COPY --from=build /opt/sky/venv /opt/sky/venv
COPY driver/ /opt/sky/driver/

# The driver never reads the network: the shim stages the base weights and
# the Hugging Face libraries run offline.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    HF_HUB_DISABLE_TELEMETRY=1 \
    SKY_CLOUD_WORK=/tmp/sky-run

# A C compiler: Triton (under Unsloth) builds its GPU launcher with the
# system's C compiler on the first step. Without one the first step fails with
# "Failed to find C compiler" (a live run on an L4, 2026-10-13). From the
# Ubuntu archive of the pinned base, not from an image registry.
RUN set -eu; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends gcc libc6-dev; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --uid 10001 --create-home --home-dir /home/sky --shell /usr/sbin/nologin sky; \
    chmod -R a+rX /opt/sky

USER 10001:10001
WORKDIR /home/sky

ENTRYPOINT ["/opt/sky/venv/bin/python", "-I", "-B", "/opt/sky/driver/sky_cloud.py"]
