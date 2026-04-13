# gooos/Makefile
# Two-step build: GNU `as` assembles boot.S, TinyGo compiles main.go to a
# relocatable object, ld.lld links both against src/linker.ld.
# Targets: build (default), iso, run, run-kernel, clean, check-multiboot

# Note: LD is overridden (not ?=) because make has a builtin LD=ld.
TINYGO ?= tinygo
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
MAIN_GO     := $(SRC_DIR)/main.go

BOOT_O      := $(TMP_DIR)/boot.o
STUBS_O     := $(TMP_DIR)/stubs.o
ISR_O       := $(TMP_DIR)/isr.o
SWITCH_O    := $(TMP_DIR)/switch.o
KERNEL_GO_O := $(TMP_DIR)/kernel_go.o
KERNEL_BIN  := $(TMP_DIR)/kernel.bin
KERNEL_ISO  := $(TMP_DIR)/kernel.iso

.PHONY: all build iso run run-kernel clean check-multiboot

all: build

build: $(KERNEL_BIN)

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

$(KERNEL_GO_O): $(MAIN_GO) $(TARGET_JSON) | $(TMP_DIR)
	$(TINYGO) build -target=$(TARGET_JSON) -o $(KERNEL_GO_O) ./$(SRC_DIR)

$(KERNEL_BIN): $(BOOT_O) $(STUBS_O) $(ISR_O) $(SWITCH_O) $(KERNEL_GO_O) $(LINKER_LD)
	$(LD) -m elf_x86_64 -n -T $(LINKER_LD) -o $(KERNEL_BIN) $(BOOT_O) $(STUBS_O) $(ISR_O) $(SWITCH_O) $(KERNEL_GO_O)

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

clean:
	rm -rf $(TMP_DIR)
