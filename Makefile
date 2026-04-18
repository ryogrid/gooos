# gooos/Makefile
# Two-step build: GNU `as` assembles boot.S, TinyGo compiles main.go to a
# relocatable object, ld.lld links both against src/linker.ld.
# Targets: build (default), iso, run, run-kernel, clean, check-multiboot

# Note: LD is overridden (not ?=) because make has a builtin LD=ld.
#
# TINYGOROOT points to a user-writable copy of the TinyGo tree so the
# kernel build can install its own bare-metal runtime files
# (runtime_gooos.go, interrupt_gooos.go) without sudo. The system
# TinyGo at /usr/local/lib/tinygo is root-owned. See
# scripts/patch_tinygo_runtime.sh and TODO.md "Deferred".
TINYGOROOT ?= $(HOME)/.local/tinygo
export TINYGOROOT
TINYGO ?= $(TINYGOROOT)/bin/tinygo
AS     ?= as
LD     := ld.lld
QEMU   ?= qemu-system-x86_64

SRC_DIR  := src
GRUB_DIR := grub
TMP_DIR  := tmp
ISO_DIR  := $(TMP_DIR)/isodir

TARGET_JSON := $(SRC_DIR)/target.json
LINKER_LD   := $(SRC_DIR)/linker.ld
BOOT_S      := $(SRC_DIR)/boot.S
STUBS_S     := $(SRC_DIR)/stubs.S
ISR_S       := $(SRC_DIR)/isr.S
SWITCH_S    := $(SRC_DIR)/switch.S
TRAMP_S     := $(SRC_DIR)/trampoline.S
TASK_S      := $(SRC_DIR)/task_stack_amd64.S
RT_ASM_S    := $(SRC_DIR)/runtime_asm_amd64.S
GO_SRCS     := $(wildcard $(SRC_DIR)/*.go)

BOOT_O      := $(TMP_DIR)/boot.o
STUBS_O     := $(TMP_DIR)/stubs.o
ISR_O       := $(TMP_DIR)/isr.o
SWITCH_O    := $(TMP_DIR)/switch.o
TRAMP_O     := $(TMP_DIR)/trampoline.o
TASK_O      := $(TMP_DIR)/task_stack_amd64.o
RT_ASM_O    := $(TMP_DIR)/runtime_asm_amd64.o
KERNEL_GO_O := $(TMP_DIR)/kernel_go.o
KERNEL_BIN  := $(TMP_DIR)/kernel.bin
KERNEL_ISO  := $(TMP_DIR)/kernel.iso

.PHONY: all build user embed-user iso run run-kernel clean check-multiboot verify-globals lint

all: build

# Build user programs, embed them as Go byte arrays, then build the kernel.
user:
	$(MAKE) -C user all

embed-user: user
	bash scripts/embed_elfs.sh

build: lint embed-user $(KERNEL_BIN) verify-globals

LINT_BIN := $(TMP_DIR)/lint_isr

$(LINT_BIN): scripts/lint_isr.go | $(TMP_DIR)
	go build -o $(LINT_BIN) ./scripts/lint_isr.go

lint: $(LINT_BIN)
	$(LINT_BIN) $(SRC_DIR)/

# verify-globals asserts TinyGo runtime queue globals (sleepQueue,
# timerQueue, runqueue) land inside [_globals_start, _globals_end)
# so the conservative collector's findGlobals scan covers them.
# A TinyGo upgrade can silently shift section layout; this guard
# fails the build before the collector starts missing live tasks.
verify-globals: $(KERNEL_BIN)
	bash scripts/verify_globals.sh $(KERNEL_BIN)

$(TMP_DIR):
	mkdir -p $(TMP_DIR)

$(BOOT_O): $(BOOT_S) | $(TMP_DIR)
	$(AS) --64 $(BOOT_S) -o $(BOOT_O)

$(STUBS_O): $(STUBS_S) | $(TMP_DIR)
	$(AS) --64 $(STUBS_S) -o $(STUBS_O)

$(ISR_O): $(ISR_S) | $(TMP_DIR)
	$(AS) --64 $(ISR_S) -o $(ISR_O)

$(SWITCH_O): $(SWITCH_S) | $(TMP_DIR)
	$(AS) --64 $(SWITCH_S) -o $(SWITCH_O)

$(TRAMP_O): $(TRAMP_S) | $(TMP_DIR)
	$(AS) --64 $(TRAMP_S) -o $(TRAMP_O)

$(TASK_O): $(TASK_S) | $(TMP_DIR)
	$(AS) --64 $(TASK_S) -o $(TASK_O)

$(RT_ASM_O): $(RT_ASM_S) | $(TMP_DIR)
	$(AS) --64 $(RT_ASM_S) -o $(RT_ASM_O)

$(KERNEL_GO_O): $(GO_SRCS) $(TARGET_JSON) | $(TMP_DIR)
	$(TINYGO) build -target=$(TARGET_JSON) -o $(KERNEL_GO_O) ./$(SRC_DIR)

$(KERNEL_BIN): $(BOOT_O) $(STUBS_O) $(ISR_O) $(SWITCH_O) $(TRAMP_O) $(TASK_O) $(RT_ASM_O) $(KERNEL_GO_O) $(LINKER_LD)
	$(LD) -m elf_x86_64 -n -T $(LINKER_LD) -o $(KERNEL_BIN) $(BOOT_O) $(STUBS_O) $(ISR_O) $(SWITCH_O) $(TRAMP_O) $(TASK_O) $(RT_ASM_O) $(KERNEL_GO_O)

check-multiboot: $(KERNEL_BIN)
	grub-file --is-x86-multiboot $(KERNEL_BIN)

iso: $(KERNEL_ISO)

$(KERNEL_ISO): $(KERNEL_BIN) $(GRUB_DIR)/grub.cfg
	rm -rf $(ISO_DIR)
	mkdir -p $(ISO_DIR)/boot/grub
	cp $(KERNEL_BIN) $(ISO_DIR)/boot/kernel.bin
	cp $(GRUB_DIR)/grub.cfg $(ISO_DIR)/boot/grub/grub.cfg
	grub-mkrescue -o $(KERNEL_ISO) $(ISO_DIR)

run: $(KERNEL_ISO) check-multiboot
	$(QEMU) -cdrom $(KERNEL_ISO) -serial stdio -no-reboot -no-shutdown

run-kernel: $(KERNEL_BIN) check-multiboot
	$(QEMU) -kernel $(KERNEL_BIN) -serial stdio -no-reboot -no-shutdown

run-smp: $(KERNEL_ISO) check-multiboot
	$(QEMU) -cdrom $(KERNEL_ISO) -serial stdio -no-reboot -no-shutdown -smp 4

# run-net attaches an emulated Intel 82540EM (e1000) NIC using QEMU's
# user-mode networking (slirp). UDP hostfwd maps:
#   host 9999/udp -> guest 7  — kernel-builtin UDP echo server
#   host 19999/udp -> guest 17 — userspace udpecho.elf (Phase 5 SDK smoke)
# Guest default IP is 10.0.2.15, gateway 10.0.2.2.
run-net: $(KERNEL_ISO) check-multiboot
	$(QEMU) -cdrom $(KERNEL_ISO) -serial stdio -no-reboot -no-shutdown \
	  -device e1000,netdev=n0 \
	  -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=udp::19999-:17

# test-net boots the kernel with an e1000 NIC under user-mode networking,
# greps serial markers (PCI/MAC/link/NET init/ARP gratuitous/ICMP+netbuf
# self-tests/UDP listener/netDiag), and round-trips a payload through
# the hostfwd UDP echo path. Exits 0 on PASS.
test-net: $(KERNEL_ISO) check-multiboot
	bash scripts/test_net.sh

# test-net-tap is the TAP-mode integration test (ping + nc against a
# live 10.0.0.2 guest). Requires root / CAP_NET_ADMIN to set up tap0.
# Not part of the per-phase gate; available for users with TAP.
test-net-tap: $(KERNEL_ISO) check-multiboot
	bash scripts/test_net_tap.sh

clean:
	rm -rf $(TMP_DIR)
	$(MAKE) -C user clean
