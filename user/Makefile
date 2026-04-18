# user/Makefile -- Build gooos user-space ELF binaries.
#
# Two-step build: TinyGo compiles Go to relocatable .o, GNU as assembles
# rt0.S, ld.lld links both against linker_user.ld to produce the final ELF.
# This mirrors the kernel's build approach (TinyGo resolves paths relative
# to its install directory, not the project root).

# Must match the patched TinyGo tree used by the kernel Makefile.
# The patched runtime_gooos_user.go / interrupt_gooos_user.go live
# under TINYGOROOT; the system tinygo at /usr/local would use its
# own (unpatched) source tree and silently produce broken ELFs.
TINYGOROOT ?= $(HOME)/.local/tinygo
TINYGO   ?= $(TINYGOROOT)/bin/tinygo
AS       ?= as
LD       := ld.lld

TARGET   := target.json
LDSCRIPT := linker_user.ld
BUILD    := build

CMDS := sh hello ls cat wc fdprobe goprobe gochan tinyc edit smpprobe udpecho dhcp
ELFS := $(patsubst %,$(BUILD)/%.elf,$(CMDS))

.PHONY: all clean

all: $(ELFS)

$(BUILD):
	mkdir -p $(BUILD)

$(BUILD)/rt0.o: rt0.S | $(BUILD)
	$(AS) --64 rt0.S -o $(BUILD)/rt0.o

# tinygo_startTask / tinygo_swapTask — required when scheduler=tasks
# is enabled. TinyGo's build pipeline does not assemble embedded .S
# files under the "tinygo build -o *.o" flow, so the stubs are
# imported here and linked per command. Mirrors the kernel's
# src/task_stack_amd64.S arrangement (see Makefile TASK_S).
$(BUILD)/task_stack_amd64.o: task_stack_amd64.S | $(BUILD)
	$(AS) --64 task_stack_amd64.S -o $(BUILD)/task_stack_amd64.o

# tinygo_longjmp — referenced by runtime._panic on amd64. TinyGo
# ships the asm under runtime/asm_amd64.S which our `-o *.o` flow
# does not assemble; mirrors src/runtime_asm_amd64.S on the kernel.
$(BUILD)/runtime_asm_amd64.o: runtime_asm_amd64.S | $(BUILD)
	$(AS) --64 runtime_asm_amd64.S -o $(BUILD)/runtime_asm_amd64.o

# Pattern rule: compile Go command + link with rt0 + task stubs +
# runtime asm.
$(BUILD)/%.elf: cmd/%/main.go gooos/*.go $(BUILD)/rt0.o $(BUILD)/task_stack_amd64.o $(BUILD)/runtime_asm_amd64.o $(LDSCRIPT) $(TARGET) | $(BUILD)
	$(TINYGO) build -target=$(TARGET) -o $(BUILD)/$*_go.o ./cmd/$*
	$(LD) -m elf_x86_64 -n -T $(LDSCRIPT) -o $@ $(BUILD)/rt0.o $(BUILD)/task_stack_amd64.o $(BUILD)/runtime_asm_amd64.o $(BUILD)/$*_go.o
	bash ../scripts/verify_globals_user.sh $@

clean:
	rm -rf $(BUILD)
