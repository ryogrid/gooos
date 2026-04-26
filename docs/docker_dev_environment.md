# Docker-based development environment

This document describes how to build the gooos kernel ISO
inside a Docker container using the `Dockerfile` shipped at
the project root. The container bundles the entire build
toolchain (apt packages, TinyGo 0.40.1 with the gooos
runtime patch pre-applied, and a fresh `git clone` of
`github.com/ryogrid/gooos` master) on top of `ubuntu:24.04`.

QEMU is **not** included in the image — the container's job
is only to produce `tmp/kernel.iso`. You run QEMU on the
host (or in any other VMM) against the extracted ISO.

## Why a Docker image?

The host-side install path documented in
[`README.md` § Prerequisites](../README.md#prerequisites)
depends on a specific set of apt packages
(`build-essential`, `grub-pc-bin`, `grub-common`, `xorriso`,
`mtools`, `lld`), the TinyGo `v0.40.1` GitHub release tarball,
and a manual patch of TinyGo's runtime tree. Years from now
some of those moving parts may no longer be readily
available — apt repos may stop carrying the exact package
versions, the TinyGo release asset may move, host distros
may drift away from Ubuntu 24.04. The Dockerfile pins the
whole stack so the build environment can be recreated on
demand from a single base image (`ubuntu:24.04`).

## What's in the image

- **Base:** `ubuntu:24.04`
- **Apt packages** (matching `README.md` § Prerequisites,
  plus a few build-time helpers):
  `build-essential`, `grub-pc-bin`, `grub-common`,
  `xorriso`, `mtools`, `lld`, `golang-go` (for the
  `make build` lint stage), `git`, `ca-certificates`,
  `wget`, `patch`, `sudo`.
- **TinyGo 0.40.1** — downloaded from
  `https://github.com/tinygo-org/tinygo/releases/download/v0.40.1/tinygo0.40.1.linux-amd64.tar.gz`,
  extracted to `/usr/local/lib/tinygo0.40.1/` (matching the
  README's host install path) and mirrored to the dev
  user's `~/.local/tinygo0.40.1/` so the `Makefile`'s
  `TINYGOROOT` default works.
- **gooos runtime patch** — `scripts/patch_tinygo_runtime.sh`
  is run during the image build so the patched runtime is
  present at first launch.
- **gooos master clone** — `https://github.com/ryogrid/gooos.git`
  at `/home/dev/gooos`.
- **Non-root user** — `dev` (uid/gid `1000:1000`) so files
  written through the bind mount come out owned by 1000:1000
  on the host (typical first non-root user on a Linux
  desktop).

QEMU is **not** included.

## Acquiring the image

There are two options:

### Option A: download the prebuilt tarball (recommended)

A prebuilt gzip-compressed image tarball is hosted at
`https://ryogird.net/dist/gooos-dev.tar.gz`. Skip the local
build (~5 minutes plus apt + tinygo download time) by
loading it directly — `docker load` reads gzip-compressed
streams transparently:

```bash
wget https://ryogird.net/dist/gooos-dev.tar.gz
docker load -i gooos-dev.tar.gz
docker images gooos-dev      # confirm the image is loaded
```

### Option B: build locally from the Dockerfile

```bash
git clone https://github.com/ryogrid/gooos.git
cd gooos
docker build -t gooos-dev .
```

The first build pulls `ubuntu:24.04`, runs `apt-get
install`, downloads the TinyGo 0.40.1 release tarball,
clones the gooos master, and applies the runtime patch.
On a typical broadband link this takes a few minutes.
Subsequent rebuilds re-use the cached lower layers; only
a `master` change re-runs the clone + patch step.

## Run the container — bind mount for ISO extraction

The container has no access to the host filesystem by
default. To get the produced ISO out of the container we
**bind-mount a host directory at `/output`** inside the
container. **The bind mount exists solely for ISO
extraction** — there is no other reason to share state with
the host in the standard workflow.

```bash
mkdir -p iso-out
docker run --rm -it \
    -v "$(pwd)/iso-out:/output" \
    gooos-dev
```

You'll land at a bash prompt as the `dev` user, with the
gooos repo at `/home/dev/gooos`:

```
dev@<container-id>:~/gooos$
```

Build the ISO and copy it out:

```bash
make iso
cp tmp/kernel.iso /output/
exit
```

Back on the host:

```bash
ls -la iso-out/kernel.iso
# -rw-r--r-- 1 1000 1000 ~7.0M ... iso-out/kernel.iso
```

### Attaching a second shell to a running container

The `--rm -it` invocation above runs the container in the
foreground; closing the shell exits the container. If you
prefer to keep the container running (for example to leave
a long-running build in one terminal and explore the source
tree in another), start it detached with a name and then
`docker exec` into it:

```bash
# Terminal 1 — start the container in the background.
docker run -d --name gooos-dev-session \
    -v "$(pwd)/iso-out:/output" \
    gooos-dev \
    sleep infinity

# Terminal 2 (or any new terminal) — open an interactive
# shell inside the running container as the dev user.
docker exec -it gooos-dev-session bash

# Open additional shells the same way — each `docker exec`
# spawns an independent process inside the container.
docker exec -it gooos-dev-session bash

# When you're finished, stop and remove the container.
docker stop gooos-dev-session
docker rm gooos-dev-session
```

If the container was started without `-d` (foreground mode),
attach a second shell from another terminal by name:

```bash
docker exec -it <container-name-or-id> bash
```

`docker ps` lists running containers and their IDs / names.
Use `docker exec -u root` if you need a root shell inside the
running container (e.g. to `apt-get install` a debug tool).

## Run QEMU on the host against the extracted ISO

The container has produced `iso-out/kernel.iso`. QEMU runs
on the host. The three standard run modes from the
top-level `Makefile` are:

```bash
# Single core (equivalent of `make run`)
qemu-system-x86_64 -cdrom iso-out/kernel.iso \
    -serial stdio -no-reboot -no-shutdown

# 4-core SMP (equivalent of `make run-smp`)
qemu-system-x86_64 -cdrom iso-out/kernel.iso \
    -serial stdio -no-reboot -no-shutdown -smp 4

# With e1000 NIC + hostfwds for the networking demos
# (equivalent of `make run-net` — see docs/networking_demos.md
# for the demo paths)
qemu-system-x86_64 -cdrom iso-out/kernel.iso \
    -serial stdio -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=udp::19999-:17,hostfwd=tcp::10080-:8080,hostfwd=tcp::10081-:8081
```

## Working on a branch other than master

Inside the container:

```bash
cd /home/dev/gooos
git fetch
git checkout <branch>
make iso
cp tmp/kernel.iso /output/
```

## Working on local changes

Two patterns:

### Pattern A — pull latest, ad-hoc test (recommended)

For most "rebuild and test" cycles, `git pull` inside the
container picks up new commits, and the existing TinyGo
patch keeps applying as long as the patch file itself is
unchanged.

```bash
cd /home/dev/gooos
git pull
# Re-run the patch script if scripts/tinygo_runtime.patch
# was updated upstream. Idempotent on an already-patched
# tree; prints `already-applied: ...` and exits 0.
bash scripts/patch_tinygo_runtime.sh
make iso
cp tmp/kernel.iso /output/
```

### Pattern B — bind-mount the host repo for active dev

For active development against an uncommitted host
working tree, bind-mount the host repo over
`/home/dev/gooos`:

```bash
docker run --rm -it \
    -v "$(pwd):/home/dev/gooos" \
    -v "$(pwd)/iso-out:/output" \
    gooos-dev
```

Caveats:

- The host clone overlays the in-image clone. The image's
  patched TinyGo tree at `/home/dev/.local/tinygo0.40.1/`
  is the only persistent toolchain bit; it survives the
  overlay.
- If `scripts/tinygo_runtime.patch` has changed in the host
  repo, re-run `bash scripts/patch_tinygo_runtime.sh`
  inside the container to land the new hunks. The script
  is idempotent.
- Bind-mount uid/gid should be 1000:1000 to match the
  in-image `dev` user. If your host user has a different
  uid, either rebuild the image with a matching uid (edit
  the `useradd -u 1000` line in the Dockerfile) or
  `chown -R 1000:1000 .` on the host repo before mounting
  (and chown back afterwards).

## Rebuilding the image

```bash
# Cache-respecting rebuild (apt + tinygo layers re-use)
docker build -t gooos-dev .

# Fully clean rebuild (re-downloads everything)
docker build --no-cache -t gooos-dev .
```

Rebuild when:

- The apt package set or TinyGo URL has changed.
- The runtime patch has been bumped and you don't want to
  re-run the patch script manually.
- Upstream master has moved a long way and you'd rather
  re-clone than `git pull` a long history.

## Troubleshooting

- **"permission denied" writing to `/output`** — the host
  bind-mount path is not writable by uid 1000. Fix with
  `chmod 777 iso-out/` on the host, or run the container
  as a host user whose uid maps cleanly inside.
- **"tinygo: command not found" / "ld.lld: command not found"**
  — the image build failed silently. Re-run
  `docker build -t gooos-dev .` and watch the apt /
  TinyGo install steps for errors.
- **"tinygo runtime patch failed"** — the script is
  idempotent on an already-patched tree (prints
  `already-applied: ...` and exits 0). A genuine failure
  means the TinyGo tree is in an unexpected shape; delete
  `/home/dev/.local/tinygo0.40.1/` inside the container,
  rerun the build steps from the relevant Dockerfile
  layer, or rebuild the image with `--no-cache`.
- **"host has uid != 1000"** — either rebuild the image
  with `useradd -u <hostuid>`, or `chown` the extracted
  ISO on the host after extraction (`sudo chown $USER
  iso-out/kernel.iso`).
- **Container exits immediately** — `docker run` without
  `-it` returns straight to the host. Use `-it` for an
  interactive shell, or supply a one-shot command:
  ```bash
  docker run --rm -v "$(pwd)/iso-out:/output" gooos-dev \
      bash -c 'make iso && cp tmp/kernel.iso /output/'
  ```
