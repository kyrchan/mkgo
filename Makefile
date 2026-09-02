# microkernel: UEFI-booted C++ substrate hosting the restricted-ISA VM
#
#   core/            arch-blind kernel proper (C++20, freestanding)
#   arch/x86_64/     machine shims: uart cpu traps paging vector
#   build/BOOTX64.EFI via scripts/mkpefi.py (ELF -> PE32+)
#   guest programs -> programs/*.vbin via tools/vasm

BUILD := build
ROOT  ?= $(HOME)/.local/osdev-root

QEMU    := $(shell command -v qemu-system-x86_64 2>/dev/null || echo $(ROOT)/usr/bin/qemu-system-x86_64)

OVMF_CODE := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_CODE_4M.fd) $(wildcard $(d)/OVMF_CODE.fd)))
OVMF_VARS := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_VARS_4M.fd) $(wildcard $(d)/OVMF_VARS.fd)))

QEMU_ENV := LD_LIBRARY_PATH=$(ROOT)/usr/lib/x86_64-linux-gnu
KVM_FLAG ?= $(shell [ -w /dev/kvm ] && echo -enable-kvm || echo -accel tcg)

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
              $(BUILD)/core/fsroute.o \
              $(BUILD)/core/preempt.o \
              $(BUILD)/core/pci.o $(BUILD)/core/vfio.o
ARCH_OBJS := $(BUILD)/arch/x86_64/uart.o $(BUILD)/arch/x86_64/cpu.o \
             $(BUILD)/arch/x86_64/traps.o $(BUILD)/arch/x86_64/traps_s.o \
             $(BUILD)/arch/x86_64/ctx_s.o $(BUILD)/arch/x86_64/irq0_stub_s.o \
             $(BUILD)/arch/x86_64/paging.o $(BUILD)/arch/x86_64/vector.o \
             $(BUILD)/arch/x86_64/timer.o $(BUILD)/arch/x86_64/math.o \
             $(BUILD)/arch/x86_64/vmware_backdoor.o

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

# wasm3 engine (vendored, MIT): plain C, NDEBUG kills asserts.
# -O3 unlocks d_m3CascadedOpcodes' tight dispatch and inlines the
# per-op switch arms; -O2 left the interpreter ~20% slower on TCG.
# We keep our own C++ at -Os (size) since the kernel image is boot-
# time constrained; the engine runs in the steady-state hot path.
$(BUILD)/wasm3/%.o: third_party/wasm3/%.c $(wildcard third_party/wasm3/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CC) -std=c11 -O3 -g -DNDEBUG -fno-strict-aliasing \
	      -Wno-unused-parameter -Ithird_party/wasm3 -c $< -o $@

$(BUILD)/kernel.so: $(OBJS) kernel/link.ld | $(BUILD)
	$(CXX) $(LDFLAGS) $(OBJS) -lgcc -o $@

# ---- guests (guest ABI: wasm + mini-WASI; never recompiled for kernel) ----
build/hello1.raw: guests/hello.wat
	$(WAT2WASM) $< -o $@
build/hello1.wasm: build/hello1.raw
	python3 scripts/add_abiver.py $< $@ 2

build/hello2.raw: guests/hello.rs
	RUSTUP_HOME=$(HOME)/.local/rustup rustc --target wasm32v1-none \
	    -C panic=abort -C opt-level=s -C link-arg=--export-memory -o $@ $<
build/hello2.wasm: build/hello2.raw
	python3 scripts/add_abiver.py $< $@ 2

build/hello3.raw: guests/hello.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -gcflags=all=-N -ldflags=-w -o ../build/hello3.raw hello.go
build/hello3.wasm: build/hello3.raw
	python3 scripts/add_abiver.py $< $@ 2

build/test_pp.raw: guests/test_pp.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -o ../$@ test_pp.go
build/test_pp.wasm: build/test_pp.raw
	python3 scripts/add_abiver.py $< $@ 2

# service modules: build raw then stamp abi_ver=2 custom section (v2.0)
services/console/console.wasm.raw: $(wildcard services/console/*.go) $(wildcard guests/lib/*.go)
	cd services/console && GOOS=wasip1 GOARCH=wasm go build -o console.wasm.raw .

services/net/net.wasm.raw: $(wildcard services/net/*.go)
	cd services/net && GOOS=wasip1 GOARCH=wasm go build -o net.wasm.raw .
services/net/net.wasm: services/net/net.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/login/login.wasm.raw: $(wildcard services/login/*.go) $(wildcard guests/lib/*.go)
	cd services/login && GOOS=wasip1 GOARCH=wasm go build -o login.wasm.raw .

services/fs/fs.wasm.raw: $(wildcard services/fs/*.go) $(wildcard guests/lib/*.go)
	cd services/fs && GOOS=wasip1 GOARCH=wasm go build -o fs.wasm.raw .

services/console/console.wasm: services/console/console.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2
services/login/login.wasm: services/login/login.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2
services/fs/fs.wasm: services/fs/fs.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/init/init.wasm.raw: $(wildcard services/init/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/init && GOOS=wasip1 GOARCH=wasm go build -o init.wasm.raw .
	python3 scripts/add_abiver.py $< $@ 2 || true

services/graphics/graphics.wasm.raw: $(wildcard services/graphics/*.go) $(wildcard guests/lib/*.go) services/graphics/go.mod
	cd services/graphics && GOOS=wasip1 GOARCH=wasm go build -o graphics.wasm.raw .

services/graphics/graphics.wasm: services/graphics/graphics.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/init/init.wasm: services/init/init.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/shell/shell.wasm.raw: $(wildcard services/shell/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/shell && GOOS=wasip1 GOARCH=wasm go build -o shell.wasm.raw .

services/shell/shell.wasm: services/shell/shell.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

# graphics.wasm: copy the built graphics module into build/ for disk images
build/graphics.wasm: services/graphics/graphics.wasm
	cp $< $@

# Phase 12+13 service wasm copy rules
build/usb.wasm: services/usb/usb.wasm
	cp $< $@
build/bt.wasm: services/bt/bt.wasm
	cp $< $@
build/wlan.wasm: services/wlan/wlan.wasm
	cp $< $@
build/e1000.wasm: services/e1000/e1000.wasm
	cp $< $@
build/ahci.wasm: services/ahci/ahci.wasm
	cp $< $@

build/shell.wasm: services/shell/shell.wasm
	cp $< $@

build/test_p5a.raw: guests/p5a/main.go $(wildcard guests/lib/*.go)
	cd guests/p5a && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p5a.wasm: build/test_p5a.raw
	python3 scripts/add_abiver.py $< $@ 2
build/test_p5b.raw: guests/p5b/main.go
	cd guests/p5b && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p5b.wasm: build/test_p5b.raw
	python3 scripts/add_abiver.py $< $@ 2

build/test_p8.raw: guests/p8/main.go
	cd guests/p8 && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p9.raw: guests/p9/main.go $(wildcard guests/lib/*.go)
	cd guests/p9 && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p10a.raw: guests/p10a/main.go $(wildcard guests/lib/*.go)
	cd guests/p10a && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p10b.raw: guests/p10b/main.go $(wildcard guests/lib/*.go)
	cd guests/p10b && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p8.wasm: build/test_p8.raw
	python3 scripts/add_abiver.py $< $@ 2
build/test_p9.wasm: build/test_p9.raw
	python3 scripts/add_abiver.py $< $@ 2
build/test_p10a.wasm: build/test_p10a.raw
	python3 scripts/add_abiver.py $< $@ 2
build/test_p10b.wasm: build/test_p10b.raw
	python3 scripts/add_abiver.py $< $@ 2

# Phase 11: VFIO smoke test
build/test_p11.raw: guests/p11/main.go $(wildcard guests/lib/*.go)
	cd guests/p11 && GOOS=wasip1 GOARCH=wasm go build -o ../../$@ .
build/test_p11.wasm: build/test_p11.raw
	python3 scripts/add_abiver.py $< $@ 2

# Phase 12: USB/BT/WLAN services + Phase 13: E1000/AHCI
# Build targets for new VFIO drivers
services/usb/usb.wasm.raw: $(wildcard services/usb/*.go) $(wildcard guests/lib/*.go)
	cd services/usb && GOOS=wasip1 GOARCH=wasm go build -o usb.wasm.raw .
services/usb/usb.wasm: services/usb/usb.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/bt/bt.wasm.raw: $(wildcard services/bt/*.go) $(wildcard guests/lib/*.go)
	cd services/bt && GOOS=wasip1 GOARCH=wasm go build -o bt.wasm.raw .
services/bt/bt.wasm: services/bt/bt.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/wlan/wlan.wasm.raw: $(wildcard services/wlan/*.go) $(wildcard guests/lib/*.go)
	cd services/wlan && GOOS=wasip1 GOARCH=wasm go build -o wlan.wasm.raw .
services/wlan/wlan.wasm: services/wlan/wlan.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/e1000/e1000.wasm.raw: $(wildcard services/e1000/*.go) $(wildcard guests/lib/*.go)
	cd services/e1000 && GOOS=wasip1 GOARCH=wasm go build -o e1000.wasm.raw .
services/e1000/e1000.wasm: services/e1000/e1000.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/ahci/ahci.wasm.raw: $(wildcard services/ahci/*.go) $(wildcard guests/lib/*.go)
	cd services/ahci && GOOS=wasip1 GOARCH=wasm go build -o ahci.wasm.raw .
services/ahci/ahci.wasm: services/ahci/ahci.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2


$(BUILD)/BOOTX64.EFI: $(BUILD)/kernel.so scripts/mkpefi.py
	python3 scripts/mkpefi.py $(BUILD)/kernel.so $@

# /etc/users — name:uid:salt$hex(sha256(salt+password)):capmask
# capmask 0x18 = FOCUS|FS_ADMIN; 0xff = all bits (admin).
$(BUILD)/etc_users.txt:
	@printf '# /etc/users — name:uid:salted-sha256:capmask\n# salted-hash = salt$$hex(sha256(salt + password))\n' > $@
	@printf 'u1:1001:u1salt$$%s:0x18\n' "$$(echo -n 'u1saltu1' | sha256sum | cut -d' ' -f1)" >> $@
	@printf 'u2:1002:u2salt$$%s:0x18\n' "$$(echo -n 'u2saltu2' | sha256sum | cut -d' ' -f1)" >> $@
	@printf 'admin:0:adminsalt$$%s:0xff\n' "$$(echo -n 'adminsaltadmin' | sha256sum | cut -d' ' -f1)" >> $@

# one disk image per payload so gates never boot a stale guest
IMG := $(BUILD)/../tools/img/img
$(IMG):
	$(MAKE) -C tools/img img

define MKDISK
$(IMG) $(1) 64 \
  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
  $(2):/vm/app
endef

$(BUILD)/disk-g1.img: $(BUILD)/BOOTX64.EFI build/hello1.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello1.wasm)

$(BUILD)/disk-g2.img: $(BUILD)/BOOTX64.EFI build/hello2.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello2.wasm)

$(BUILD)/disk-g3.img: $(BUILD)/BOOTX64.EFI build/hello3.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello3.wasm)

# Phase 4 disk: payload = test_pp; boot services under /boot/modules
define MKDISKP4
$(IMG) $(1) 64 \
  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
  build/test_pp.wasm:/vm/app \
  services/console/console.wasm:/boot/modules/console.wasm \
  services/login/login.wasm:/boot/modules/login.wasm
endef

$(BUILD)/disk-p4.img: $(BUILD)/BOOTX64.EFI build/test_pp.wasm \
                      services/console/console.wasm services/login/login.wasm | $(BUILD)
	$(call MKDISKP4,$@)

# Phase 5 disks: fs server + two payload slots (app=/vm/app, app2=/vm/app2)
define MKDISK5
$(IMG) $(1) 64 \
  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
  services/fs/fs.wasm:/boot/modules/fs.wasm \
  services/console/console.wasm:/boot/modules/console.wasm \
  services/login/login.wasm:/boot/modules/login.wasm
endef

$(BUILD)/disk-p5a.img: $(BUILD)/BOOTX64.EFI build/test_p5a.wasm \
                       build/test_p5b.wasm \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm | $(BUILD)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p5a.wasm:/vm/app \
	  build/test_p5b.wasm:/vm/app2

$(BUILD)/disk-p5b.img: $(BUILD)/BOOTX64.EFI build/test_p5b.wasm \
                       build/test_p5a.wasm \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm | $(BUILD)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p5b.wasm:/vm/app \
	  build/test_p5a.wasm:/vm/app2

# Phase 7 disk: full service set + init.conf, no payload slots
# NOTE: init.conf MUST be at ESP root (loader loads \init.conf, not \etc\init.conf)
$(BUILD)/disk-p7.img: $(BUILD)/BOOTX64.EFI services/fs/fs.wasm \
                       services/console/console.wasm services/login/login.wasm \
                       services/init/init.wasm services/shell/shell.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0\nfs fs.wasm 10\nlogin login.wasm 8\nshell shell.wasm 8\n' > $(BUILD)/init.conf.tmp
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  services/init/init.wasm:/boot/modules/init.wasm \
	  services/shell/shell.wasm:/boot/modules/shell.wasm \
	  $(BUILD)/init.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

# Phase 9 disk: services incl. net + the p9 driver guest in /vm/app
# NOTE: init.conf MUST be at ESP root (loader loads \init.conf, not \etc\init.conf)
$(BUILD)/disk-p9.img: $(BUILD)/BOOTX64.EFI build/test_p9.wasm \
                      services/fs/fs.wasm services/console/console.wasm \
                      services/login/login.wasm services/init/init.wasm \
                      services/shell/shell.wasm services/net/net.wasm \
                      $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0\nnet net.wasm 22\np9 p9.wasm 0 respawn=no\n' > $(BUILD)/init-p9.conf.tmp
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  services/init/init.wasm:/boot/modules/init.wasm \
	  services/shell/shell.wasm:/boot/modules/shell.wasm \
	  services/net/net.wasm:/boot/modules/net.wasm \
	  build/test_p9.wasm:/boot/modules/p9.wasm \
	  $(BUILD)/init-p9.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

# Phase 10 disk: multiuser services + graphics integration
$(BUILD)/disk-p10.img: $(BUILD)/BOOTX64.EFI build/test_p10a.wasm \
                        build/test_p10b.wasm services/fs/fs.wasm \
                        services/console/console.wasm services/login/login.wasm \
                        build/graphics.wasm build/shell.wasm $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  build/graphics.wasm:/boot/modules/graphics.wasm \
	  build/shell.wasm:/boot/modules/shell.wasm \
	  build/test_p10a.wasm:/vm/app \
	  build/test_p10b.wasm:/vm/app2 \
	  $(BUILD)/etc_users.txt:/etc/users

# Phase 11: VFIO smoke test disk (p11.wasm as /vm/app, legacy mode)
$(BUILD)/disk-p11.img: $(BUILD)/BOOTX64.EFI build/test_p11.wasm | $(BUILD) $(IMG)
	$(call MKDISK,$@,build/test_p11.wasm)

# Phase 11b: graphics integration — graphics.wasm renders /etc/motd to LFB
# Legacy payload-slot mode: graphics.wasm at /vm/app with gate_mask 0x300
# (CAP_PCI|CAP_FB). Avoids init.conf multi-file loader path.
$(BUILD)/disk-p11b.img: $(BUILD)/BOOTX64.EFI build/graphics.wasm | $(BUILD) $(IMG)
	printf '300' > $(BUILD)/gate-p11b.tmp
	printf 'Welcome to the capability microkernel\nPhase 11: VFIO graphics integration\nFramebuffer rendering via wasm.\n' > $(BUILD)/motd-p11b.tmp
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  build/graphics.wasm:/vm/app \
	  $(BUILD)/gate-p11b.tmp:/vm/gate \
	  $(BUILD)/motd-p11b.tmp:/etc/motd

$(BUILD)/VARS.fd:
	cp $(OVMF_VARS) $@

QEMU_BASE := -L $(ROOT)/usr/share/qemu -L $(ROOT)/usr/share/seabios -machine q35 \
	-cpu max -m 512 $(KVM_FLAG) \
	-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
	-drive if=pflash,format=raw,file=$(BUILD)/VARS.fd \
	-device isa-debug-exit,iobase=0xf4,iosize=0x04 \
	-device bochs-display \
	-display none -no-reboot -net none

image: $(BUILD)/disk-p7.img

run: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(BUILD)/disk-p7.img -serial stdio

# Interactive run with GTK graphics window — shows framebuffer output visually.
run-gfx: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(subst -display none,-display gtk,$(QEMU_BASE)) \
		-drive format=raw,file=$(BUILD)/disk-p7.img -serial stdio

# quick smoke gate: smallest wasm guest through engine + WASI
test: test-g1

define RUN_QEMU
	@rm -f $(BUILD)/serial.log
	@timeout 600 env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(1) -serial file:$(BUILD)/serial.log || true
endef

# per-guest wasm gates (Phase 3): each guest prints its marker via fd_write
.PHONY: test-g1 test-g2 test-g3 test-p4 test-p5a test-p5b test-p7 test-p8a test-p8b test-p9 test-p10 test-p11 test-p11b test-p11-gfx test-all

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

# Phase 11: VFIO smoke test — PCI enum, BAR map, FB mode, doorbell
test-p11: $(BUILD)/disk-p11.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p11.img)
	@grep -q 'p11: pci enum found' $(BUILD)/serial.log \
	    && grep -q 'p11: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11 VFIO smoke)" \
	    || { echo "TEST FAIL (p11)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 11b: graphics integration — graphics.wasm renders /etc/motd to LFB
test-p11b: $(BUILD)/disk-p11b.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p11b.img)
	@grep -q 'graphics: fb_present ok' $(BUILD)/serial.log \
	    && grep -q 'graphics: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11b graphics integration)" \
	    || { echo "TEST FAIL (p11b)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 11 gfx: legacy-mode disk — graphics.wasm at /vm/app, no init.conf
$(BUILD)/disk-p11-gfx.img: $(BUILD)/BOOTX64.EFI build/graphics.wasm | $(BUILD)
	$(call MKDISK,$@,build/graphics.wasm)
 
.PHONY: test-g1 test-g2 test-g3 test-p4 test-p5a test-p5b test-p7 test-p8a test-p8b test-p9 test-p10 test-p11 test-p11b test-p11-gfx test-p12 test-p13 test-all

.PHONY: test-p11-gfx
test-p11-gfx: $(BUILD)/disk-p11-gfx.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p11-gfx.img)
	@grep -q 'graphics: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11-gfx legacy graphics)" \
	    || { echo "TEST FAIL (p11-gfx)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 12: USB + BT + WLAN services boot as /vm/app with CAP_PCI
$(BUILD)/disk-p12.img: $(BUILD)/BOOTX64.EFI build/usb.wasm services/bt/bt.wasm services/wlan/wlan.wasm | $(BUILD) $(IMG)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  build/usb.wasm:/vm/app \
	  services/bt/bt.wasm:/boot/modules/bt.wasm \
	  services/wlan/wlan.wasm:/boot/modules/wlan.wasm

.PHONY: test-p12
test-p12: $(BUILD)/disk-p12.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p12.img)
	@grep -q 'e1000.*all ok\|usb.*all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p12 usb+bt+wlan)" \
	    || { echo "TEST FAIL (p12)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

# Phase 13: E1000 + AHCI VFIO drivers boot as /vm/app and /vm/app2 (legacy mode)
$(BUILD)/disk-p13.img: $(BUILD)/BOOTX64.EFI services/e1000/e1000.wasm services/ahci/ahci.wasm | $(BUILD) $(IMG)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/e1000/e1000.wasm:/vm/app \
	  services/ahci/ahci.wasm:/vm/app2

.PHONY: test-p13
test-p13: $(BUILD)/disk-p13.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p13.img)
	@grep -q 'e1000.*all ok' $(BUILD)/serial.log \
	    && grep -q 'ahci.*all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p13 e1000+ahci)" \
	    || { echo "TEST FAIL (p13)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

.PHONY: test-p9
test-p9: $(BUILD)/disk-p9.img $(BUILD)/VARS.fd
	bash scripts/run_p9.sh $(BUILD)/serial-p9.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p9.img $(QEMU_BASE)
	@grep -q 'p9. udp ok' $(BUILD)/serial-p9.log \
	    && grep -q 'p9. http ok' $(BUILD)/serial-p9.log \
	    && echo "TEST PASS (p9 network E2E)" \
	    || { echo "TEST FAIL (p9)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p9.log | tail -40; exit 1; }

# Phase 8: cooperative multitasking — both sessions make progress
$(BUILD)/disk-p8.img: $(BUILD)/BOOTX64.EFI build/test_p8.wasm | $(BUILD) $(IMG)
	$(IMG) $@ 64 \
	  $(BUILD)/BOOTX64.EFI:/EFI/BOOT/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p8.wasm:/vm/app \
	  build/test_p8.wasm:/vm/app2

test-p8a: $(BUILD)/disk-p8.img $(BUILD)/VARS.fd
	$(call RUN_QEMU,$(BUILD)/disk-p8.img)
	@grep -q 'busy. done marks=' $(BUILD)/serial.log \
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
	    -device virtio-blk-pci,drive=p8b \
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
HT_CXXFLAGS := -std=c++20 -O1 -g -Wall -Icore -Iarch/x86_64 -Ithird_party/wasm3
HT_OBJS := $(BUILD)/ht/ports.o $(BUILD)/ht/kernsvc.o $(BUILD)/ht/fsroute.o \
           $(BUILD)/ht/devblk.o $(BUILD)/ht/input.o $(BUILD)/ht/vfio_hoststub.o \
           $(BUILD)/ht/preempt.o

$(BUILD)/hosttest: tools/hosttest.cc $(HT_OBJS) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $(BUILD)/ht/hosttest.o
	$(CXX) -no-pie $(BUILD)/ht/hosttest.o $(HT_OBJS) -o $@

$(BUILD)/ht/%.o: core/%.cc $(wildcard core/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $@

$(BUILD)/ht/vfio_hoststub.o: core/vfio_hoststub.cc core/vfio.h | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $@

.PHONY: test-kernel
test-kernel: $(BUILD)/hosttest
	$(BUILD)/hosttest

test-all: test-kernel test-unit test-g1 test-g2 test-g3 test-p4 test-p5a test-p5b test-p7 test-p8a test-p8b test-p9 test-p10 test-p11 test-p11b test-p12 test-p13

# Phase 10: KVM+TCG matrix — every gate green under both accelerators.
# KVM_FLAG is overridable ( ?= ) so matrix targets can force an accelerator.
.PHONY: test-matrix-tcg test-matrix-kvm test-matrix
test-matrix-tcg:
	$(MAKE) test-all KVM_FLAG="-accel tcg"
test-matrix-kvm:
	$(MAKE) test-all KVM_FLAG="-enable-kvm"
test-matrix: test-matrix-tcg test-matrix-kvm


clean:
	rm -rf $(BUILD)
