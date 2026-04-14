# user/Makefile -- Build gooos user-space ELF binaries.
#
# Two-step build: TinyGo compiles Go to relocatable .o, GNU as assembles
# rt0.S, ld.lld links both against linker_user.ld to produce the final ELF.
# This mirrors the kernel's build approach (TinyGo resolves paths relative
# to its install directory, not the project root).

TINYGO   ?= tinygo
AS       ?= as
LD       := ld.lld

TARGET   := target.json
LDSCRIPT := linker_user.ld
BUILD    := build

CMDS := sh hello ls cat wc
ELFS := $(patsubst %,$(BUILD)/%.elf,$(CMDS))

.PHONY: all clean

all: $(ELFS)

$(BUILD):
	mkdir -p $(BUILD)

$(BUILD)/rt0.o: rt0.S | $(BUILD)
	$(AS) --64 rt0.S -o $(BUILD)/rt0.o

# Pattern rule: compile Go command + link with rt0.
$(BUILD)/%.elf: cmd/%/main.go gooos/*.go $(BUILD)/rt0.o $(LDSCRIPT) $(TARGET) | $(BUILD)
	$(TINYGO) build -target=$(TARGET) -o $(BUILD)/$*_go.o ./cmd/$*
	$(LD) -m elf_x86_64 -n -T $(LDSCRIPT) -o $@ $(BUILD)/rt0.o $(BUILD)/$*_go.o

clean:
	rm -rf $(BUILD)
