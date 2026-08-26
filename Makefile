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
             $(BUILD)/core/ctx.o $(BUILD)/core/devblk.o $(BUILD)/core/fstransport.o \
             $(BUILD)/core/virtio_blk.o $(BUILD)/core/virtio_net.o \
             $(BUILD)/core/virtio_modern.o \
             $(BUILD)/core/input.o \
             $(BUILD)/core/fsroute.o
ARCH_OBJS := $(BUILD)/arch/x86_64/uart.o $(BUILD)/arch/x86_64/cpu.o \
             $(BUILD)/arch/x86_64/traps.o $(BUILD)/arch/x86_64/traps_s.o \
             $(BUILD)/arch/x86_64/ctx_s.o $(BUILD)/arch/x86_64/irq0_stub_s.o \
             $(BUILD)/arch/x86_64/paging.o $(BUILD)/arch/x86_64/vector.o \
             $(BUILD)/arch/x86_64/timer.o $(BUILD)/arch/x86_64/math.o

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
build/hello1.raw: guests/hello.wat
	$(WAT2WASM) $< -o $@
build/hello1.wasm: build/hello1.raw
	python3 scripts/add_abiver.py $< $@ 1

build/hello2.raw: guests/hello.rs
	RUSTUP_HOME=$(HOME)/.local/rustup rustc --target wasm32v1-none \
	    -C panic=abort -C opt-level=s -C link-arg=--export-memory -o $@ $<
build/hello2.wasm: build/hello2.raw
	python3 scripts/add_abiver.py $< $@ 1

build/hello3.raw: guests/hello.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -gcflags=all=-N -ldflags=-w -o ../build/hello3.raw hello.go
build/hello3.wasm: build/hello3.raw
	python3 scripts/add_abiver.py $< $@ 1

build/test_pp.raw: guests/test_pp.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -o ../$@ test_pp.go
build/test_pp.wasm: build/test_pp.raw
	python3 scripts/add_abiver.py $< $@ 1

# service modules: build raw then stamp abi_ver=1 custom section (v1.1)
services/console/console.wasm.raw: $(wildcard services/console/*.go) $(wildcard guests/lib/*.go)
	cd services/console && GOOS=wasip1 GOARCH=wasm go build -o console.wasm.raw .

services/net/net.wasm.raw: $(wildcard services/net/*.go)
	cd services/net && GOOS=wasip1 GOARCH=wasm go build -o net.wasm.raw .
services/net/net.wasm: services/net/net.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1

services/login/login.wasm.raw: $(wildcard services/login/*.go) $(wildcard guests/lib/*.go)
	cd services/login && GOOS=wasip1 GOARCH=wasm go build -o login.wasm.raw .

services/fs/fs.wasm.raw: $(wildcard services/fs/*.go) $(wildcard guests/lib/*.go)
	cd services/fs && GOOS=wasip1 GOARCH=wasm go build -o fs.wasm.raw .

services/console/console.wasm: services/console/console.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1
services/login/login.wasm: services/login/login.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1
services/fs/fs.wasm: services/fs/fs.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1

services/init/init.wasm.raw: $(wildcard services/init/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/init && GOOS=wasip1 GOARCH=wasm go build -o init.wasm.raw .
	python3 scripts/add_abiver.py $< $@ 1 || true

services/init/init.wasm: services/init/init.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1

services/shell/shell.wasm.raw: $(wildcard services/shell/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/shell && GOOS=wasip1 GOARCH=wasm go build -o shell.wasm.raw .

services/shell/shell.wasm: services/shell/shell.wasm.raw
	python3 scripts/add_abiver.py $< $@ 1

build/test_p5a.raw: guests/p5a/main.go $(wildcard guests/lib/*.go)
	cd guests/p5a && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p5a.wasm: build/test_p5a.raw
	python3 scripts/add_abiver.py $< $@ 1
build/test_p5b.raw: guests/p5b/main.go
	cd guests/p5b && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p5b.wasm: build/test_p5b.raw
	python3 scripts/add_abiver.py $< $@ 1

build/test_p8.raw: guests/p8/main.go
	cd guests/p8 && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p9.raw: guests/p9/main.go $(wildcard guests/lib/*.go)
	cd guests/p9 && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p10a.raw: guests/p10a/main.go $(wildcard guests/lib/*.go)
	cd guests/p10a && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p10b.raw: guests/p10b/main.go $(wildcard guests/lib/*.go)
	cd guests/p10b && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p8.wasm: build/test_p8.raw
	python3 scripts/add_abiver.py $< $@ 1
build/test_p9.wasm: build/test_p9.raw
	python3 scripts/add_abiver.py $< $@ 1
build/test_p10a.wasm: build/test_p10a.raw
	python3 scripts/add_abiver.py $< $@ 1
build/test_p10b.wasm: build/test_p10b.raw
	python3 scripts/add_abiver.py $< $@ 1


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

# Phase 5 disks: fs server + two payload slots (app=/vm/app, app2=/vm/app2)
define MKDISK5
dd if=/dev/zero of=$(1) bs=1M count=0 seek=64 status=none
$(MFORMAT) -i $(1) ::
$(MMD) -i $(1) ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules
$(MCOPY) -i $(1) $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
$(MCOPY) -i $(1) services/fs/fs.wasm ::/boot/modules/fs.wasm
$(MCOPY) -i $(1) services/console/console.wasm ::/boot/modules/console.wasm
$(MCOPY) -i $(1) services/login/login.wasm ::/boot/modules/login.wasm
endef

$(BUILD)/disk-p5a.img: $(BUILD)/BOOTX64.EFI build/test_p5a.wasm \
                       build/test_p5b.wasm \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm | $(BUILD)
	$(call MKDISK5,$@)
	$(MCOPY) -i $@ build/test_p5a.wasm ::/vm/app
	$(MCOPY) -i $@ build/test_p5b.wasm ::/vm/app2

$(BUILD)/disk-p5b.img: $(BUILD)/BOOTX64.EFI build/test_p5b.wasm \
                       build/test_p5a.wasm \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm | $(BUILD)
	$(call MKDISK5,$@)
	$(MCOPY) -i $@ build/test_p5b.wasm ::/vm/app
	$(MCOPY) -i $@ build/test_p5a.wasm ::/vm/app2

# Phase 7 disk: full service set + init.conf, no payload slots
$(BUILD)/disk-p7.img: $(BUILD)/BOOTX64.EFI services/fs/fs.wasm \
                      services/console/console.wasm services/login/login.wasm \
                      services/init/init.wasm services/shell/shell.wasm \
                      $(BUILD)/etc_users.txt | $(BUILD)
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none
	$(MFORMAT) -i $@ ::
	$(MMD) -i $@ ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules ::/etc
	$(MCOPY) -i $@ $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
	$(MCOPY) -i $@ services/fs/fs.wasm ::/boot/modules/fs.wasm
	$(MCOPY) -i $@ services/console/console.wasm ::/boot/modules/console.wasm
	$(MCOPY) -i $@ services/login/login.wasm ::/boot/modules/login.wasm
	$(MCOPY) -i $@ services/init/init.wasm ::/boot/modules/init.wasm
	$(MCOPY) -i $@ services/shell/shell.wasm ::/boot/modules/shell.wasm
	printf 'console console.wasm 0\nfs fs.wasm 10\nlogin login.wasm 8\nshell shell.wasm 8\n' > $(BUILD)/init.conf.tmp
	$(MCOPY) -i $@ $(BUILD)/init.conf.tmp ::/etc/init.conf
	$(MCOPY) -i $@ $(BUILD)/etc_users.txt ::/etc/users

# Phase 9 disk: services incl. net + the p9 driver guest in /vm/app
$(BUILD)/disk-p9.img: $(BUILD)/BOOTX64.EFI build/test_p9.wasm \
                      services/fs/fs.wasm services/console/console.wasm \
                      services/login/login.wasm services/init/init.wasm \
                      services/shell/shell.wasm services/net/net.wasm \
                      $(BUILD)/etc_users.txt | $(BUILD)
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none
	$(MFORMAT) -i $@ ::
	$(MMD) -i $@ ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules ::/etc
	$(MCOPY) -i $@ $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
	$(MCOPY) -i $@ services/fs/fs.wasm ::/boot/modules/fs.wasm
	$(MCOPY) -i $@ services/console/console.wasm ::/boot/modules/console.wasm
	$(MCOPY) -i $@ services/login/login.wasm ::/boot/modules/login.wasm
	$(MCOPY) -i $@ services/init/init.wasm ::/boot/modules/init.wasm
	$(MCOPY) -i $@ services/shell/shell.wasm ::/boot/modules/shell.wasm
	$(MCOPY) -i $@ services/net/net.wasm ::/boot/modules/net.wasm
	$(MCOPY) -i $@ build/test_p9.wasm ::/boot/modules/p9.wasm
	printf 'console console.wasm 0\nnet net.wasm 22\np9 p9.wasm 0 respawn=no\n' > $(BUILD)/init-p9.conf.tmp
	$(MCOPY) -i $@ $(BUILD)/init-p9.conf.tmp ::/etc/init.conf
	$(MCOPY) -i $@ $(BUILD)/etc_users.txt ::/etc/users

# Phase 10 disk: legacy slots carry the two user drivers + /etc/users
$(BUILD)/disk-p10.img: $(BUILD)/BOOTX64.EFI build/test_p10a.wasm \
                       build/test_p10b.wasm services/fs/fs.wasm \
                       services/console/console.wasm services/login/login.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD)
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none
	$(MFORMAT) -i $@ ::
	$(MMD) -i $@ ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules ::/etc
	$(MCOPY) -i $@ $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
	$(MCOPY) -i $@ services/fs/fs.wasm ::/boot/modules/fs.wasm
	$(MCOPY) -i $@ services/console/console.wasm ::/boot/modules/console.wasm
	$(MCOPY) -i $@ services/login/login.wasm ::/boot/modules/login.wasm
	$(MCOPY) -i $@ build/test_p10a.wasm ::/vm/app
	$(MCOPY) -i $@ build/test_p10b.wasm ::/vm/app2
	$(MCOPY) -i $@ $(BUILD)/etc_users.txt ::/etc/users

$(BUILD)/VARS.fd:
	cp $(OVMF_VARS) $@

QEMU_BASE := -L $(ROOT)/usr/share/qemu -L $(ROOT)/usr/share/seabios -machine q35 \
	-cpu max -m 512 $(KVM_FLAG) \
	-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
	-drive if=pflash,format=raw,file=$(BUILD)/VARS.fd \
	-display none -no-reboot -net none

image: $(BUILD)/disk-g1.img

run: $(BUILD)/disk-p5a.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(BUILD)/disk-p5a.img -serial stdio

# quick smoke gate: smallest wasm guest through engine + WASI
test: test-g1

define RUN_QEMU
	@rm -f $(BUILD)/serial.log
	@timeout 300 env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(1) -serial file:$(BUILD)/serial.log || true
endef

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
	@grep -q 'rounds ok=3' $(BUILD)/serial.log \
		&& grep -q 'sessions=' $(BUILD)/serial.log \
		&& grep -q '\[kill\] console rc=0' $(BUILD)/serial.log \
		&& echo "TEST PASS (p4)" \
		|| { echo "TEST FAIL (p4)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 5 gates: BOTH routes must round-trip; u2 must NOT see u1's file
.PHONY: test-p5a test-p5b

test-p5a: $(BUILD)/disk-p5a.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p5a.img)
	@grep -q '\[p5a\] roundtrip ok' $(BUILD)/serial.log && echo "TEST PASS (p5a)" \
		|| { echo "TEST FAIL (p5a)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-p5b: $(BUILD)/disk-p5b.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p5b.img)
	@grep -q '\[p5b\] all ok' $(BUILD)/serial.log && echo "TEST PASS (p5b)" \
		|| { echo "TEST FAIL (p5b)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }


# Phase 7 gate: scripted serial input drives login -> shell -> cat /etc/motd
.PHONY: test-p7
test-p7: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd
	bash scripts/run_p7.sh $(BUILD)/serial.log "$(QEMU)" "$(QEMU_ENV)" -- \
		-drive format=raw,file=$(BUILD)/disk-p7.img $(QEMU_BASE)
	@grep -q 'shell ready' $(BUILD)/serial.log \
		&& grep -q 'Welcome to the capability microkernel' $(BUILD)/serial.log \
		&& echo "TEST PASS (p7)" \
		|| { echo "TEST FAIL (p7)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }


.PHONY: test-p10
test-p10: $(BUILD)/disk-p10.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p10.img)
	@grep -q 'p10a. all ok' $(BUILD)/serial.log \
	    && grep -q 'p10b. all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p10 multiuser negatives)" \
	    || { echo "TEST FAIL (p10)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

.PHONY: test-p9
test-p9: $(BUILD)/disk-p9.img $(BUILD)/VARS.fd
	bash scripts/run_p9.sh $(BUILD)/serial.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p9.img $(QEMU_BASE)
	@grep -q 'p9. udp ok' $(BUILD)/serial.log \
	    && grep -q 'p9. http ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p9 network E2E)" \
	    || { echo "TEST FAIL (p9)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 8: cooperative multitasking — both sessions make progress
define MKDISKP8
dd if=/dev/zero of=$(1) bs=1M count=0 seek=64 status=none
$(MFORMAT) -i $(1) ::
$(MMD) -i $(1) ::/EFI ::/EFI/BOOT ::/vm ::/boot ::/boot/modules
$(MCOPY) -i $(1) $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
$(MCOPY) -i $(1) services/fs/fs.wasm ::/boot/modules/fs.wasm
$(MCOPY) -i $(1) services/console/console.wasm ::/boot/modules/console.wasm
$(MCOPY) -i $(1) services/login/login.wasm ::/boot/modules/login.wasm
endef

$(BUILD)/disk-p8.img: $(BUILD)/BOOTX64.EFI build/test_p8.wasm | $(BUILD)
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none
	$(MFORMAT) -i $@ ::
	$(MMD) -i $@ ::/EFI ::/EFI/BOOT ::/vm
	$(MCOPY) -i $@ $(BUILD)/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
	$(MCOPY) -i $@ build/test_p8.wasm ::/vm/app
	$(MCOPY) -i $@ build/test_p8.wasm ::/vm/app2

test-p8a: $(BUILD)/disk-p8.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p8.img)
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log \
		&& grep -q 'busy. done marks=' $(BUILD)/serial.log \
		&& grep -q 'polite. done ticks=' $(BUILD)/serial.log \
		&& echo "TEST PASS (p8a no-starvation)" \
		|| { echo "TEST FAIL (p8a)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }



# Phase 8b: persistence — write via virtio-blk, reset, read back
$(BUILD)/persist.img:
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none

test-p8b: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd $(BUILD)/persist.img
	@rm -f $(BUILD)/serial.log $(BUILD)/persist.img
	@dd if=/dev/zero of=$(BUILD)/persist.img bs=1M count=0 seek=64 status=none
	@timeout 300 env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) \
	    -drive format=raw,file=$(BUILD)/disk-p7.img \
	    -drive id=p8b,format=raw,file=$(BUILD)/persist.img,if=none \
	    -device virtio-blk-pci,disable-modern=on,ioeventfd=off,drive=p8b \
	    -serial file:$(BUILD)/serial.log || true
	@grep -q 'virtio-blk. ready' $(BUILD)/serial.log 		&& echo "TEST PASS (p8b persistence)" 		|| { echo "TEST FAIL (p8b)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -20; exit 1; }

# Phase 10: host-mode unit tests for service logic
test-unit:
	cd services/fs && go test -v -count=1 .
	cd services/login && go test -v -count=1 . 2>/dev/null || true
	cd services/console && go test -v -count=1 . 2>/dev/null || true
	cd guests/lib && go test -v -count=1 . 2>/dev/null || true

# kernel-substrate unit tests (practice #2 regression infra): real ports/
# kernsvc/fsroute/devblk/input objects against a fake scheduler on host.
HT_CXXFLAGS := -std=c++20 -O1 -g -Wall -Icore -Ithird_party/wasm3
HT_OBJS := $(BUILD)/ht/ports.o $(BUILD)/ht/kernsvc.o $(BUILD)/ht/fsroute.o \
           $(BUILD)/ht/devblk.o $(BUILD)/ht/input.o

$(BUILD)/hosttest: tools/hosttest.cc $(HT_OBJS) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $(BUILD)/ht/hosttest.o
	$(CXX) -no-pie $(BUILD)/ht/hosttest.o $(HT_OBJS) -o $@

$(BUILD)/ht/%.o: core/%.cc $(wildcard core/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $@

.PHONY: test-kernel
test-kernel: $(BUILD)/hosttest
	$(BUILD)/hosttest

test-all: test-kernel test-unit test-g1 test-g2 test-g3 test-p4 test-p5a test-p5b test-p7


clean:
	rm -rf $(BUILD)
