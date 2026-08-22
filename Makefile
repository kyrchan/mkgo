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
            -Icore -Iarch/x86_64 -Ithird_party/wasm3
ASFLAGS :=
LDFLAGS := -nostdlib -no-pie -Wl,--build-id=none -Wl,-e,efi_main -T kernel/link.ld

CORE_OBJS := $(BUILD)/core/main.o $(BUILD)/core/kmain.o $(BUILD)/core/lib.o \
             $(BUILD)/core/loader.o $(BUILD)/core/mm.o $(BUILD)/core/rt.o \
             $(BUILD)/core/engine.o $(BUILD)/core/wasi_glue.o \
             $(BUILD)/core/sched.o $(BUILD)/core/ports.o $(BUILD)/core/kernsvc.o \
             $(BUILD)/core/ctx.o $(BUILD)/core/vm/vm.o
ARCH_OBJS := $(BUILD)/arch/x86_64/uart.o $(BUILD)/arch/x86_64/cpu.o \
             $(BUILD)/arch/x86_64/traps.o $(BUILD)/arch/x86_64/traps_s.o \
             $(BUILD)/arch/x86_64/paging.o $(BUILD)/arch/x86_64/vector.o \
             $(BUILD)/arch/x86_64/timer.o $(BUILD)/arch/x86_64/math.o \
             $(BUILD)/arch/x86_64/ctx_s.o
WASM3_SRC := $(wildcard third_party/wasm3/*.c)
WASM3_OBJS := $(patsubst %.c,$(BUILD)/wasm3/%.o,$(notdir $(WASM3_SRC)))
OBJS := $(CORE_OBJS) $(ARCH_OBJS) $(WASM3_OBJS)

WAT2WASM := $(HOME)/.local/wabt/bin/wat2wasm

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

$(BUILD)/arch/x86_64/%_s.o: arch/x86_64/%.S | $(BUILD)
	@mkdir -p $(dir $@)
	$(CC) $(ASFLAGS) -c $< -o $@

# wasm3 engine (vendored, MIT): plain C, NDEBUG kills asserts
$(BUILD)/wasm3/%.o: third_party/wasm3/%.c $(wildcard third_party/wasm3/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CC) -std=c11 -O2 -g -DNDEBUG -fno-strict-aliasing \
	      -Wno-unused-parameter -Ithird_party/wasm3 -c $< -o $@

$(BUILD)/kernel.so: $(OBJS) kernel/link.ld | $(BUILD)
	$(CXX) $(LDFLAGS) $(OBJS) -lgcc -o $@

# ---- guests (guest ABI: wasm + mini-WASI; never recompiled for kernel) ----
build/hello1.wasm: guests/hello.wat
	$(WAT2WASM) $< -o $@

build/hello2.wasm: guests/hello.rs
	RUSTUP_HOME=$(HOME)/.local/rustup rustc --target wasm32v1-none \
	    -C panic=abort -C opt-level=s -C link-arg=--export-memory -o $@ $<

build/hello3.wasm: guests/hello.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -gcflags=all=-N -ldflags=-w -o ../$@ hello.go

services/console/console.wasm: services/console/main.go
	cd services/console && GOOS=wasip1 GOARCH=wasm go build -o console.wasm main.go

services/login/login.wasm: services/login/main.go
	cd services/login && GOOS=wasip1 GOARCH=wasm go build -o login.wasm main.go

build/test_pp.wasm: guests/test_pp.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -o ../$@ test_pp.go


$(BUILD)/vasm: $(wildcard tools/vasm/*.go) | $(BUILD)
	cd tools/vasm && go build -o ../../$@ .

tools: $(BUILD)/vasm

programs/demo.vbin: programs/demo.vasm $(BUILD)/vasm
	$(BUILD)/vasm $< $@

$(BUILD)/BOOTX64.EFI: $(BUILD)/kernel.so scripts/mkpefi.py
	python3 scripts/mkpefi.py $(BUILD)/kernel.so $@

# one disk image per payload so gates never boot a stale guest
define MKDISK
dd if=/dev/zero of=$(1) bs=1M count=0 seek=64 status=none
$(MFORMAT) -i $(1) ::
$(MMD) -i $(1) ::/EFI ::/EFI/BOOT ::/vm
$(MCOPY) -i $(1) $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
$(MCOPY) -i $(1) $(2) ::/vm/app
endef

$(BUILD)/disk.img: $(BUILD)/BOOTX64.EFI programs/demo.vbin | $(BUILD)
	$(call MKDISK,$@,programs/demo.vbin)

$(BUILD)/disk-g1.img: $(BUILD)/BOOTX64.EFI build/hello1.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello1.wasm)

$(BUILD)/disk-g2.img: $(BUILD)/BOOTX64.EFI build/hello2.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello2.wasm)

$(BUILD)/disk-g3.img: $(BUILD)/BOOTX64.EFI build/hello3.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello3.wasm)

# Phase 4 disk: payload = test_pp; boot services under /boot/modules
define MKDISKP4
dd if=/dev/zero of=$(1) bs=1M count=0 seek=64 status=none
$(MFORMAT) -i $(1) ::
$(MMD) -i $(1) ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules
$(MCOPY) -i $(1) $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
$(MCOPY) -i $(1) build/test_pp.wasm ::/vm/app
$(MCOPY) -i $(1) services/console/console.wasm ::/boot/modules/console.wasm
$(MCOPY) -i $(1) services/login/login.wasm ::/boot/modules/login.wasm
endef

$(BUILD)/disk-p4.img: $(BUILD)/BOOTX64.EFI build/test_pp.wasm \
                      services/console/console.wasm services/login/login.wasm | $(BUILD)
	$(call MKDISKP4,$@)

image: $(BUILD)/disk.img

$(BUILD)/VARS.fd:
	cp $(OVMF_VARS) $@

QEMU_BASE := -L $(ROOT)/usr/share/qemu -L $(ROOT)/usr/share/seabios -machine q35 \
	-cpu max -m 512 $(KVM_FLAG) \
	-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
	-drive if=pflash,format=raw,file=$(BUILD)/VARS.fd \
	-display none -no-reboot -net none

run: image $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(BUILD)/disk.img -serial stdio

define RUN_QEMU
	@rm -f $(BUILD)/serial.log
	@timeout 120 env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(1) -serial file:$(BUILD)/serial.log || true
endef

test: image $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -qE 'out 0x0*28' $(BUILD)/serial.log \
		&& echo "TEST PASS" || { echo "TEST FAIL"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

# per-guest wasm gates (Phase 3): each guest prints its marker via fd_write
.PHONY: test-g1 test-g2 test-g3 test-all

test-g1: $(BUILD)/disk-g1.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-g1.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -q 'hello from C' \
		$(BUILD)/serial.log && echo "TEST PASS (g1)" \
		|| { echo "TEST FAIL (g1)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-g2: $(BUILD)/disk-g2.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-g2.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -q 'hello from Rust' \
		$(BUILD)/serial.log && echo "TEST PASS (g2)" \
		|| { echo "TEST FAIL (g2)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-g3: $(BUILD)/disk-g3.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-g3.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -q 'hello from Go' \
		$(BUILD)/serial.log && echo "TEST PASS (g3)" \
		|| { echo "TEST FAIL (g3)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-p4: $(BUILD)/disk-p4.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p4.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log \
		&& grep -q 'rounds ok=3' $(BUILD)/serial.log \
		&& grep -q 'sessions=' $(BUILD)/serial.log \
		&& grep -q '\[kill\] console rc=0' $(BUILD)/serial.log \
		&& echo "TEST PASS (p4)" \
		|| { echo "TEST FAIL (p4)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-all: test test-g1 test-g2 test-g3 test-p4

clean:
	rm -rf $(BUILD)
