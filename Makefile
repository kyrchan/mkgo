# microkernel: UEFI-booted C++ substrate hosting the restricted-ISA VM
#
#   core/            arch-blind kernel proper (C++20, freestanding)
#   arch/x86_64/     machine shims: uart cpu traps paging vector
#   build/BOOTX64.EFI via scripts/mkpefi.py (ELF -> PE32+)
#   guest programs -> programs/*.vbin via tools/vasm

BUILD := build
ROOT  ?= $(HOME)/.local/osdev-root

QEMU    := $(shell command -v qemu-system-x86_64 2>/dev/null || echo $(ROOT)/usr/bin/qemu-system-x86_64)
MFORMAT := $(shell command -v mformat 2>/dev/null || echo $(ROOT)/usr/bin/mformat)
MMD     := $(shell command -v mmd 2>/dev/null || echo $(ROOT)/usr/bin/mmd)
MCOPY   := $(shell command -v mcopy 2>/dev/null || echo $(ROOT)/usr/bin/mcopy)

OVMF_CODE := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_CODE_4M.fd) $(wildcard $(d)/OVMF_CODE.fd)))
OVMF_VARS := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_VARS_4M.fd) $(wildcard $(d)/OVMF_VARS.fd)))

QEMU_ENV := LD_LIBRARY_PATH=$(ROOT)/usr/lib/x86_64-linux-gnu
KVM_FLAG := $(shell [ -w /dev/kvm ] && echo -enable-kvm || echo -accel tcg)

CC      := gcc
CXX     := g++
CXXFLAGS := -std=c++20 -ffreestanding -fno-exceptions -fno-rtti \
            -fno-threadsafe-statics -fno-stack-protector -fno-stack-clash-protection \
            -mno-red-zone -fno-pic -mcmodel=small -Os -g -Wall -Wextra \
            -Icore -Iarch/x86_64
ASFLAGS :=
LDFLAGS := -nostdlib -no-pie -Wl,--build-id=none -Wl,-e,efi_main -T kernel/link.ld

CORE_OBJS := $(BUILD)/core/main.o $(BUILD)/core/kmain.o $(BUILD)/core/lib.o \
             $(BUILD)/core/loader.o $(BUILD)/core/mm.o $(BUILD)/core/vm/vm.o
ARCH_OBJS := $(BUILD)/arch/x86_64/uart.o $(BUILD)/arch/x86_64/cpu.o \
             $(BUILD)/arch/x86_64/traps.o $(BUILD)/arch/x86_64/traps_s.o \
             $(BUILD)/arch/x86_64/paging.o $(BUILD)/arch/x86_64/vector.o
OBJS := $(CORE_OBJS) $(ARCH_OBJS)

.PHONY: all run test clean image tools

all: image

$(BUILD):
	mkdir -p $@

$(BUILD)/core/%.o: core/%.cc $(wildcard core/*.h core/vm/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -c $< -o $@

$(BUILD)/arch/x86_64/%.o: arch/x86_64/%.cc $(wildcard arch/x86_64/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -c $< -o $@

# vector ops lower 1:1 onto AVX2; must be compiled with ymm-capable codegen
$(BUILD)/arch/x86_64/vector.o: arch/x86_64/vector.cc $(wildcard arch/x86_64/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -mavx2 -c $< -o $@

$(BUILD)/arch/x86_64/traps_s.o: arch/x86_64/traps.S | $(BUILD)
	@mkdir -p $(dir $@)
	$(CC) $(ASFLAGS) -c $< -o $@

$(BUILD)/kernel.so: $(OBJS) kernel/link.ld | $(BUILD)
	$(CXX) $(LDFLAGS) $(OBJS) -o $@

$(BUILD)/vasm: $(wildcard tools/vasm/*.go) | $(BUILD)
	cd tools/vasm && go build -o ../../$@ .

tools: $(BUILD)/vasm

programs/demo.vbin: programs/demo.vasm $(BUILD)/vasm
	$(BUILD)/vasm $< $@

$(BUILD)/BOOTX64.EFI: $(BUILD)/kernel.so scripts/mkpefi.py
	python3 scripts/mkpefi.py $(BUILD)/kernel.so $@

$(BUILD)/disk.img: $(BUILD)/BOOTX64.EFI programs/demo.vbin | $(BUILD)
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none
	$(MFORMAT) -i $@ ::
	$(MMD) -i $@ ::/EFI ::/EFI/BOOT ::/vm
	$(MCOPY) -i $@ $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
	$(MCOPY) -i $@ programs/demo.vbin ::/vm/prog.vbin

image: $(BUILD)/disk.img

$(BUILD)/VARS.fd:
	cp $(OVMF_VARS) $@

QEMU_ARGS := -L $(ROOT)/usr/share/qemu -L $(ROOT)/usr/share/seabios -machine q35 \
	-cpu max -m 512 $(KVM_FLAG) \
	-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
	-drive if=pflash,format=raw,file=$(BUILD)/VARS.fd \
	-drive format=raw,file=$(BUILD)/disk.img \
	-display none -no-reboot -net none

run: image $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_ARGS) -serial stdio

test: image $(BUILD)/VARS.fd
	@rm -f $(BUILD)/serial.log
	@timeout 120 env $(QEMU_ENV) $(QEMU) $(QEMU_ARGS) -serial file:$(BUILD)/serial.log || true
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -qE 'out 0x0*28' $(BUILD)/serial.log \
		&& echo "TEST PASS" || { echo "TEST FAIL"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

clean:
	rm -rf $(BUILD)
