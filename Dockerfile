# syntax=docker/dockerfile:1
#
# gooos build environment — Ubuntu 24.04 + apt build toolchain
# + TinyGo 0.40.1 (with the gooos runtime patch pre-applied)
# + a fresh `git clone` of github.com/ryogrid/gooos master.
#
# Designed for long-term reproducibility against apt-repo /
# TinyGo-release decay. QEMU is intentionally NOT included —
# the container only produces tmp/kernel.iso; the user runs
# QEMU on the host. See docs/docker_dev_environment.md for
# the full walkthrough.
#
# Build:   docker build -t gooos-dev .
# Run:     docker run --rm -it -v "$(pwd)/iso-out:/output" gooos-dev
# Inside:  make iso && cp tmp/kernel.iso /output/

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Etc/UTC

# Build toolchain. Mirrors the apt one-liner in README.md
# § Prerequisites, plus golang-go for the make-build lint
# stage (scripts/lint_isr.go) and patch / git / wget /
# ca-certificates for the TinyGo runtime patch + master
# clone + tarball download. QEMU intentionally omitted.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential \
        grub-pc-bin \
        grub-common \
        xorriso \
        mtools \
        lld \
        golang-go \
        git \
        ca-certificates \
        wget \
        patch \
        sudo \
 && rm -rf /var/lib/apt/lists/*

# Non-root dev user (uid/gid 1000). Files written via the
# bind-mounted /output then come out as 1000:1000 on the
# host — matching the typical first non-root user on a
# Linux desktop. Ubuntu 24.04's base image already ships a
# default 'ubuntu' user at uid 1000; remove it first so
# 'dev' can take that slot.
RUN userdel -r ubuntu 2>/dev/null || true \
 && groupadd -g 1000 dev \
 && useradd -m -u 1000 -g 1000 -s /bin/bash dev

# TinyGo 0.40.1 — pinned URL per project requirement.
# The tarball extracts to a top-level `tinygo/` directory;
# rename to `tinygo0.40.1/` so it matches README.md's
# install-path convention. Then mirror to the dev user's
# home so the Makefile's TINYGOROOT default
# ($(HOME)/.local/tinygo0.40.1) and the patch script's
# expected location both line up.
RUN wget -qO /tmp/tinygo.tar.gz \
        https://github.com/tinygo-org/tinygo/releases/download/v0.40.1/tinygo0.40.1.linux-amd64.tar.gz \
 && tar -xzf /tmp/tinygo.tar.gz -C /usr/local/lib \
 && mv /usr/local/lib/tinygo /usr/local/lib/tinygo0.40.1 \
 && rm /tmp/tinygo.tar.gz \
 && mkdir -p /home/dev/.local \
 && cp -a /usr/local/lib/tinygo0.40.1 /home/dev/.local/tinygo0.40.1 \
 && chown -R dev:dev /home/dev/.local

# /output is the canonical mount point for ISO extraction.
# Created up-front and chown'd so the bind-mount fallback
# (no -v) works. A bind-mounted host directory overrides
# this at run time.
RUN mkdir -p /output && chown dev:dev /output

# Switch to dev for the clone + patch + default working dir.
USER dev
WORKDIR /home/dev

# Clone the upstream master and pin to a known-good commit
# (the current master HEAD at image-build time). Pinning
# guards against unrelated upstream churn invalidating the
# build between rebuilds. Bump GOOOS_REV when rebuilding the
# image against a newer master tip after verifying the
# scripts/tinygo_runtime.patch still applies cleanly to a
# fresh TinyGo 0.40.1 tree.
ARG GOOOS_REV=916318433f93c564e24debd5e786d011005f3ffb
RUN git clone https://github.com/ryogrid/gooos.git \
 && git -C gooos checkout "${GOOOS_REV}"

# Apply the TinyGo runtime patch (idempotent, no sudo —
# operates on /home/dev/.local/tinygo0.40.1, owned by dev).
WORKDIR /home/dev/gooos
RUN bash scripts/patch_tinygo_runtime.sh

WORKDIR /home/dev/gooos
CMD ["/bin/bash"]
