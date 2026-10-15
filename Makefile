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
            -Icore -Iarch/x86_64
WASM3_CFLAGS := -Icore
ASFLAGS := -fno-pic -mcmodel=small -fno-stack-protector -fno-stack-clash-protection -mno-red-zone
LDFLAGS := -nostdlib -no-pie -Wl,--build-id=none -Wl,-e,efi_main -T kernel/link.ld

 CORE_OBJS := $(BUILD)/core/main.o $(BUILD)/core/kmain.o $(BUILD)/core/lib.o \
                $(BUILD)/core/loader.o $(BUILD)/core/mm.o $(BUILD)/core/log.o $(BUILD)/core/rt.o \
                $(BUILD)/core/engine.o \
                $(BUILD)/core/sched.o $(BUILD)/core/ports.o $(BUILD)/core/kernsvc.o \
                $(BUILD)/core/cap_table.o \
                $(BUILD)/core/ctx.o $(BUILD)/core/devblk.o $(BUILD)/core/fsimport.o \
                $(BUILD)/core/acpi.o \
               $(BUILD)/core/virtio_blk.o $(BUILD)/core/virtio_net.o \
               $(BUILD)/core/virtio_modern.o \
               $(BUILD)/core/input.o \
               $(BUILD)/core/fsroute.o \
               $(BUILD)/core/preempt.o \
               $(BUILD)/core/pci.o $(BUILD)/core/vfio.o \
               $(BUILD)/core/perf.o $(BUILD)/core/sha256.o \
                $(BUILD)/core/wasm/wasm.o $(BUILD)/core/wasm/owned_session.o \
                $(BUILD)/core/wasm/wasi_bridge.o
 ARCH_OBJS := $(BUILD)/arch/x86_64/uart.o $(BUILD)/arch/x86_64/cpu.o \
              $(BUILD)/arch/x86_64/traps.o $(BUILD)/arch/x86_64/traps_s.o \
              $(BUILD)/arch/x86_64/ctx_s.o $(BUILD)/arch/x86_64/irq0_stub_s.o \
              $(BUILD)/arch/x86_64/paging.o $(BUILD)/arch/x86_64/vector.o \
              $(BUILD)/arch/x86_64/timer.o $(BUILD)/arch/x86_64/math.o \
              $(BUILD)/arch/x86_64/vmware_backdoor.o \
              $(BUILD)/arch/x86_64/lock.o $(BUILD)/arch/x86_64/iommu.o \
              $(BUILD)/arch/x86_64/mp.o $(BUILD)/arch/x86_64/mp_s.o \
              $(BUILD)/arch/x86_64/trampoline_s.o

OBJS := $(CORE_OBJS) $(ARCH_OBJS)

WAT2WASM := $(HOME)/.local/wabt/bin/wat2wasm

.PHONY: all run test clean image tools test-wasm-interp test-wasm-artifacts test-wasm-fuzz test-wasm-owned test-wasm-wasi test-wasm-wasi-artifacts test-g1-owned test-g2-owned test-g3-owned test-p8-owned

all: image

$(BUILD):
	mkdir -p $@

$(BUILD)/core/%.o: core/%.cc $(wildcard core/*.h core/vm/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -c $< -o $@

$(BUILD)/core/wasm/%.o: core/wasm/%.cc $(wildcard core/wasm/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -c $< -o $@

$(BUILD)/arch/x86_64/%.o: arch/x86_64/%.cc $(wildcard arch/x86_64/*.h) $(wildcard core/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -c $< -o $@

# vector ops lower 1:1 onto AVX2; must be compiled with ymm-capable codegen
$(BUILD)/arch/x86_64/vector.o: arch/x86_64/vector.cc $(wildcard arch/x86_64/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) -mavx2 -c $< -o $@

$(BUILD)/arch/x86_64/%_s.o: arch/x86_64/%.S | $(BUILD)
	@mkdir -p $(dir $@)
	$(CC) $(ASFLAGS) -c $< -o $@

$(BUILD)/kernel.so: $(OBJS) kernel/link.ld | $(BUILD)
	$(CXX) $(LDFLAGS) $(OBJS) -lgcc -o $@

# ---- guests (guest ABI: wasm + mini-WASI; never recompiled for kernel) ----
build/hello1.raw: guests/hello.wat
	$(WAT2WASM) $< -o $@
build/hello1.wasm: build/hello1.raw
	python3 scripts/add_abiver.py $< $@ 2

$(BUILD)/owned.marker:
	@printf 'owned\n' > $@

build/hello2.raw: guests/hello.rs
	RUSTUP_HOME=$(HOME)/.local/rustup rustc --target wasm32v1-none \
	    -C panic=abort -C opt-level=s -C link-arg=--export-memory -o $@ $<
build/hello2.wasm: build/hello2.raw
	python3 scripts/add_abiver.py $< $@ 2

build/hello3.raw: guests/hello.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -gcflags=all=-N -ldflags=-w -o ../build/hello3.raw hello.go
build/hello3.wasm: build/hello3.raw
	python3 scripts/add_abiver.py $< $@ 2

build/test_pp.raw: guests/test_pp.go
	cd guests && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../$@ test_pp.go
build/test_pp.wasm: build/test_pp.raw
	python3 scripts/add_abiver.py $< $@ 2

build/tui.raw: guests/tui/main.go
	cd guests/tui && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ main.go
build/tui.wasm: build/tui.raw
	python3 scripts/add_abiver.py $< $@ 2

# service modules: build raw then stamp abi_ver=2 custom section (v2.0)
services/console/console.wasm.raw: $(wildcard services/console/*.go) $(wildcard guests/lib/*.go)
	cd services/console && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o console.wasm.raw .

services/net/net.wasm.raw: $(wildcard services/net/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/net && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o net.wasm.raw .
services/net/net.wasm: services/net/net.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/login/login.wasm.raw: $(wildcard services/login/*.go) $(wildcard guests/lib/*.go)
	cd services/login && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o login.wasm.raw .

services/fs/fs.wasm.raw: $(wildcard services/fs/*.go) $(wildcard guests/lib/*.go)
	cd services/fs && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o fs.wasm.raw .

services/console/console.wasm: services/console/console.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2
services/login/login.wasm: services/login/login.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2
services/fs/fs.wasm: services/fs/fs.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/init/init.wasm.raw: $(wildcard services/init/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/init && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o init.wasm.raw .

services/graphics/graphics.wasm.raw: $(wildcard services/graphics/*.go) $(wildcard services/graphics/compositor/*.go) $(wildcard guests/lib/*.go) services/graphics/go.mod
	cd services/graphics && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o graphics.wasm.raw .

services/graphics/graphics.wasm: services/graphics/graphics.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/init/init.wasm: services/init/init.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/shell/shell.wasm.raw: $(wildcard services/shell/*.go) $(wildcard guests/lib/*.go) services/go.mod
	cd services/shell && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o shell.wasm.raw .

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

build/p8b.wasm: guests/p8b/p8b.wasm
	python3 scripts/add_abiver.py $< $@ 2

build/shell.wasm: services/shell/shell.wasm
	cp $< $@

build/test_p5a.raw: guests/p5a/main.go $(wildcard guests/lib/*.go)
	cd guests/p5a && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p5a.wasm: build/test_p5a.raw
	python3 scripts/add_abiver.py $< $@ 2
build/test_p5b.raw: guests/p5b/main.go
	cd guests/p5b && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p5b.wasm: build/test_p5b.raw
	python3 scripts/add_abiver.py $< $@ 2

build/test_p8.raw: guests/p8/main.go
	cd guests/p8 && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p9.raw: guests/p9/main.go $(wildcard guests/lib/*.go)
	cd guests/p9 && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p10a.raw: guests/p10a/main.go $(wildcard guests/lib/*.go)
	cd guests/p10a && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p10b.raw: guests/p10b/main.go $(wildcard guests/lib/*.go)
	cd guests/p10b && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
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
	cd guests/p11 && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ../../$@ .
build/test_p11.wasm: build/test_p11.raw
	python3 scripts/add_abiver.py $< $@ 2

# Phase 12: USB/BT/WLAN services + Phase 13: E1000/AHCI
# Build targets for new VFIO drivers
services/usb/usb.wasm.raw: $(wildcard services/usb/*.go) $(wildcard guests/lib/*.go)
	cd services/usb && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o usb.wasm.raw .
services/usb/usb.wasm: services/usb/usb.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/bt/bt.wasm.raw: $(wildcard services/bt/*.go) $(wildcard guests/lib/*.go)
	cd services/bt && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o bt.wasm.raw .
services/bt/bt.wasm: services/bt/bt.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/wlan/wlan.wasm.raw: $(wildcard services/wlan/*.go) $(wildcard guests/lib/*.go)
	cd services/wlan && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o wlan.wasm.raw .
services/wlan/wlan.wasm: services/wlan/wlan.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/e1000/e1000.wasm.raw: $(wildcard services/e1000/*.go) $(wildcard guests/lib/*.go)
	cd services/e1000 && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o e1000.wasm.raw .
services/e1000/e1000.wasm: services/e1000/e1000.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/ahci/ahci.wasm.raw: $(wildcard services/ahci/*.go) $(wildcard guests/lib/*.go)
	cd services/ahci && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ahci.wasm.raw .
services/ahci/ahci.wasm: services/ahci/ahci.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/display/display.wasm.raw: $(wildcard services/display/*.go) $(wildcard guests/lib/*.go)
	cd services/display && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o display.wasm.raw .
services/display/display.wasm: services/display/display.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2

services/ssh/ssh.wasm.raw: $(wildcard services/ssh/*.go) $(wildcard guests/lib/*.go)
	cd services/ssh && GOOS=wasip1 GOARCH=wasm go build -buildvcs=false -o ssh.wasm.raw .
services/ssh/ssh.wasm: services/ssh/ssh.wasm.raw
	python3 scripts/add_abiver.py $< $@ 2


$(BUILD)/BOOTX64.EFI: $(BUILD)/kernel.so scripts/mkpefi.py
	python3 scripts/mkpefi.py $(BUILD)/kernel.so $@

# /etc/users — name:uid:salt$hex(sha256(salt+password)):capmask
# capmask 0x18 = FOCUS|FS_ADMIN; 0xff = all bits (admin).
$(BUILD)/etc_users.txt:
	@printf '# /etc/users — name:uid:salted-sha256:capmask\n# salted-hash = salt$$hex(sha256(salt + password))\n' > $@
	@printf 'u1:1001:u1salt$$%s:0x18\n' "$$(echo -n 'u1saltu1' | sha256sum | cut -d' ' -f1)" >> $@
	@printf 'u2:1002:u2salt$$%s:0x18\n' "$$(echo -n 'u2saltu2' | sha256sum | cut -d' ' -f1)" >> $@
	@printf 'admin:0:adminsalt$$%s:0x1fff\n' "$$(echo -n 'adminsaltadmin' | sha256sum | cut -d' ' -f1)" >> $@

IMG := tools/img/img
# Floor for check-makefile-intact. Set from a healthy tree; lower it
# only when targets are really removed, never to silence a gate.
MAKEFILE_MIN_LINES = 1435
# Depend on the builder's sources: without this, editing tools/img/*.go left
# the binary in place and every disk-*.img kept being built by the old
# image writer -- the "stale objects cause silent wrong-image bugs" class.
$(IMG): $(wildcard tools/img/*.go) $(wildcard tools/img/go.mod)
	$(MAKE) -C tools/img img

 # one disk image per payload so gates never boot a stale guest
 define MKDISK
 $(IMG) -o $(1) -size 64M -efi $(BUILD)/BOOTX64.EFI \
   $(2):/vm/app
 endef

$(BUILD)/disk-g1.img: $(BUILD)/BOOTX64.EFI build/hello1.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello1.wasm)

$(BUILD)/disk-g1-owned.img: $(BUILD)/BOOTX64.EFI build/hello1.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/hello1.wasm:/vm/app \
	  $(BUILD)/owned.marker:/vm/owned

$(BUILD)/disk-g2-owned.img: $(BUILD)/BOOTX64.EFI build/hello2.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/hello2.wasm:/vm/app \
	  $(BUILD)/owned.marker:/vm/owned

$(BUILD)/disk-g3-owned.img: $(BUILD)/BOOTX64.EFI build/hello3.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/hello3.wasm:/vm/app \
	  $(BUILD)/owned.marker:/vm/owned

$(BUILD)/disk-g2.img: $(BUILD)/BOOTX64.EFI build/hello2.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello2.wasm)

$(BUILD)/disk-g3.img: $(BUILD)/BOOTX64.EFI build/hello3.wasm | $(BUILD)
	$(call MKDISK,$@,build/hello3.wasm)

 # Phase 4 disk: payload = test_pp; boot services under /boot/modules
 define MKDISKP4
 $(IMG) -o $(1) -size 64M -efi $(BUILD)/BOOTX64.EFI \
   services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
   build/test_pp.wasm:/vm/app
 endef

$(BUILD)/disk-p4.img: $(BUILD)/BOOTX64.EFI build/test_pp.wasm \
                       services/console/console.wasm services/login/login.wasm | $(BUILD)
	$(call MKDISKP4,$@)

$(BUILD)/disk-p4-owned.img: $(BUILD)/BOOTX64.EFI build/test_pp.wasm \
                              services/console/console.wasm services/login/login.wasm \
                              $(BUILD)/owned.marker | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_pp.wasm:/vm/app \
	  $(BUILD)/owned.marker:/vm/owned

$(BUILD)/disk-p5a.img: $(BUILD)/BOOTX64.EFI build/test_p5a.wasm \
                        build/test_p5b.wasm \
                        services/fs/fs.wasm services/console/console.wasm \
                        services/login/login.wasm | $(BUILD)
	printf '1000\n' > $(BUILD)/gate-p5b.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p5a.wasm:/vm/app \
	  build/test_p5b.wasm:/vm/app2 \
	  $(BUILD)/gate-p5b.tmp:/vm/gate2

$(BUILD)/disk-p5b.img: $(BUILD)/BOOTX64.EFI build/test_p5b.wasm \
                        build/test_p5a.wasm \
                        services/fs/fs.wasm services/console/console.wasm \
                        services/login/login.wasm | $(BUILD)
	printf '1000\n' > $(BUILD)/gate-p5b.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p5b.wasm:/vm/app \
	  build/test_p5a.wasm:/vm/app2 \
	  $(BUILD)/gate-p5b.tmp:/vm/gate2

 # Phase 7 disk: full service set + init.conf, no payload slots
 # NOTE: init.conf MUST be at ESP root (loader loads \init.conf, not \etc\init.conf)
$(BUILD)/disk-p7.img: $(BUILD)/BOOTX64.EFI services/fs/fs.wasm \
                        services/console/console.wasm services/login/login.wasm \
                        services/init/init.wasm services/shell/shell.wasm \
                        build/tui.wasm \
                        $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nlogin login.wasm 0x1008\nshell shell.wasm 0x1008\n' > $(BUILD)/init.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  services/init/init.wasm:/boot/modules/init.wasm \
	  services/shell/shell.wasm:/boot/modules/shell.wasm \
	  build/tui.wasm:/vm/tui.wasm \
	  $(BUILD)/init.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

$(BUILD)/disk-p7-owned.img: $(BUILD)/BOOTX64.EFI services/fs/fs.wasm \
                              services/console/console.wasm services/login/login.wasm \
                              services/init/init.wasm services/shell/shell.wasm \
                              build/tui.wasm \
                              $(BUILD)/etc_users.txt $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nlogin login.wasm 0x1008\nshell shell.wasm 0x1008\n' > $(BUILD)/init-owned.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm \
	  services/console/console.wasm:/boot/modules/console.wasm \
	  services/login/login.wasm:/boot/modules/login.wasm \
	  services/init/init.wasm:/boot/modules/init.wasm \
	  services/shell/shell.wasm:/boot/modules/shell.wasm \
	  build/tui.wasm:/vm/tui.wasm \
	  $(BUILD)/init-owned.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users \
	  $(BUILD)/owned.marker:/vm/owned

 # Phase 9 disk: services incl. net + the p9 driver guest in /vm/app
 # NOTE: init.conf MUST be at ESP root (loader loads \init.conf, not \etc\init.conf)
$(BUILD)/disk-p9.img: $(BUILD)/BOOTX64.EFI build/test_p9.wasm \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm services/init/init.wasm \
                       services/shell/shell.wasm services/net/net.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	# p9 binds the well-known "net" name, so it needs CAP_PORTBIND
	# (ABI 7.1) like console/net do. init spawns exactly the mask in
	# this file, so a 0 here made the driver spin on denied PORT_BIND.
	printf 'console console.wasm 0x1000\nnet net.wasm 0x1022\np9 p9.wasm 0x1000 respawn=no\n' > $(BUILD)/init-p9.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm services/net/net.wasm:/boot/modules/net.wasm \
	  build/test_p9.wasm:/boot/modules/p9.wasm \
	  $(BUILD)/init-p9.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

 # Phase 10 disk: multiuser services + graphics integration
$(BUILD)/disk-p10.img: $(BUILD)/BOOTX64.EFI build/test_p10a.wasm \
                         build/test_p10b.wasm services/fs/fs.wasm \
                         services/console/console.wasm services/login/login.wasm \
                         build/graphics.wasm build/shell.wasm $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	  build/graphics.wasm:/boot/modules/graphics.wasm \
	  build/shell.wasm:/boot/modules/shell.wasm \
	  build/test_p10a.wasm:/vm/app \
	  build/test_p10b.wasm:/vm/app2 \
	  $(BUILD)/etc_users.txt:/etc/users

 # Phase 11: VFIO smoke test disk (p11.wasm as /vm/app, legacy mode)
# gate_mask 0x1103 = KILL|PORTBIND|DEVMAN|PCI. Without DEVMAN the
# devman ENUM is denied and the guest reports "pci enum found 0", which is
# what this gate used to accept: the markers were printed unconditionally,
# so a device bus with nothing on it passed. PCI is needed for the BAR/IRQ
# steps the guest exercises after enumeration.
$(BUILD)/disk-p11.img: $(BUILD)/BOOTX64.EFI build/test_p11.wasm | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p11.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/test_p11.wasm:/vm/app \
	  $(BUILD)/gate-p11.tmp:/vm/gate

# Owned-engine twins: the same image plus the /vm/owned marker, so every
# session in it runs on the project-owned interpreter instead of wasm3.
$(BUILD)/disk-p11-owned.img: $(BUILD)/BOOTX64.EFI build/test_p11.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p11-owned.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/test_p11.wasm:/vm/app \
	  $(BUILD)/owned.marker:/vm/owned \
	  $(BUILD)/gate-p11-owned.tmp:/vm/gate

 # Phase 11b: graphics integration — graphics.wasm renders /etc/motd to LFB
 # Legacy payload-slot mode: graphics.wasm at /vm/app with gate_mask 0x300
 # (CAP_PCI|CAP_FB). Avoids init.conf multi-file loader path.
$(BUILD)/disk-p11b.img: $(BUILD)/BOOTX64.EFI build/graphics.wasm | $(BUILD) $(IMG)
	printf '300' > $(BUILD)/gate-p11b.tmp
	printf 'Welcome to the capability microkernel\nPhase 11: VFIO graphics integration\nFramebuffer rendering via wasm.\n' > $(BUILD)/motd-p11b.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/graphics.wasm:/vm/app \
	  $(BUILD)/gate-p11b.tmp:/vm/gate \
	  $(BUILD)/motd-p11b.tmp:/etc/motd

$(BUILD)/disk-p11b-owned.img: $(BUILD)/BOOTX64.EFI build/graphics.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf '300' > $(BUILD)/gate-p11b.tmp
	printf 'Welcome to the capability microkernel\nPhase 11: VFIO graphics integration\nFramebuffer rendering via wasm.\n' > $(BUILD)/motd-p11b.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/graphics.wasm:/vm/app \
	  $(BUILD)/gate-p11b.tmp:/vm/gate \
	  $(BUILD)/motd-p11b.tmp:/etc/motd \
	  $(BUILD)/owned.marker:/vm/owned

# Per-gate OVMF variable store. Gates used to share ONE writable
# build/VARS.fd. OVMF records a triple fault as "Boot#### failed" in
# that store, so one wedged gate made every LATER gate boot the
# firmware path instead of the kernel -- a silent 0-byte serial log
# that looked like a kernel hang. Each gate now gets its own pristine
# copy, so gates cannot poison each other and a rerun is reproducible.
VARS ?= $(BUILD)/VARS.fd

$(BUILD)/vars/%.fd: | $(BUILD)
	@mkdir -p $(BUILD)/vars
	@rm -f $@ && cp $(OVMF_VARS) $@

$(BUILD)/VARS.fd:
	cp $(OVMF_VARS) $@

# NOTE: recursively expanded (=, not :=) so the per-gate VARS override above
# is picked up when the recipe runs.
# QEMU_MEM is the guest RAM for a gate. 512 MiB is enough again: the
# owned engine lowers function bodies on first call, and only the ~9% of
# functions a session actually enters keep IR resident, so a booted
# six-service fleet uses ~208 MiB of pool (measured, [sh] pool total=
# ... used=217814560 at 1 GiB; the same fleet is green at 512 MiB, and
# the full 34-gate matrix passes there).
#
# History, because it looks like a regression when it happens again:
# this was 1024 from the wasm3 cutover, when the engine held lowered IR
# for every function at ~10-12x each module's blob (22-29 MiB per Go
# service, 83 MiB for shell.wasm). At 512 MiB back then p10 failed at
# "map_bar: guest memory grow failed need=0x5400000". net.wasm's frozen
# 64 MiB window still wants ~72 MiB of guest memory for that mapping.
# Machine property, not a behaviour change, overridable per gate.
QEMU_MEM ?= 512

# Real PCIe devices for the gates that claim to pass a NIC and an HBA
# through. Phase 13's gate used to run with no e1000 and no AHCI
# controller attached at all, and both guests printed their success
# markers from the "device not found" path, so the gate could not fail.
P13_PCIE = -netdev user,id=p13net -device e1000,netdev=p13net -device ich9-ahci,id=ahci0 \
	-device qemu-xhci,id=xhci0

QEMU_BASE = -L $(ROOT)/usr/share/qemu -L $(ROOT)/usr/share/seabios -machine q35 \
	-cpu max -m $(QEMU_MEM) $(KVM_FLAG) \
	-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
	-drive if=pflash,format=raw,file=$(VARS) \
	-device isa-debug-exit,iobase=0xf4,iosize=0x04 \
	-device bochs-display \
	-display none -no-reboot -net none

image: $(BUILD)/disk-p7.img

run: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(BUILD)/disk-p7.img -serial stdio

# Interactive run with GTK graphics window. Uses the p11b image
# (graphics.wasm as the legacy payload slot with CAP_PCI|CAP_FB
# granted directly by the kernel), which actually populates the
# framebuffer. The p7 image used by `make run` is the multi-service
# interactive session and does not drive the LFB.
# Interactive boot with a NIC: disk-p16.img is the only image whose
# init.conf starts net.wasm, so it is the only one where the shell's
# ping/nc/http can reach anything. QEMU_BASE alone has no virtio-net --
# that is why "ping: net unavailable" is the answer on run/run-smp.
run-net: $(BUILD)/disk-p16.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) $(NETDEV_ARGS) \
		-drive format=raw,file=$(BUILD)/disk-p16.img -serial stdio

run-gfx: $(BUILD)/disk-p11b.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(subst -display none,-display gtk,$(QEMU_BASE)) \
		-drive format=raw,file=$(BUILD)/disk-p11b.img -serial stdio

# SMP run: 4 vCPUs so the AP bring-up path is exercised. Each AP runs
# its own cooperative-under-interrupt scheduler over its own session
# pool; no session migrates between cores.
SMP_NCPU ?= 4
run-smp: $(BUILD)/disk-p7.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -smp $(SMP_NCPU) \
		-drive format=raw,file=$(BUILD)/disk-p7.img -serial stdio

run-gfx-smp: $(BUILD)/disk-p11b.img $(BUILD)/VARS.fd
	env $(QEMU_ENV) $(QEMU) $(subst -display none,-display gtk,$(QEMU_BASE)) -smp $(SMP_NCPU) \
		-drive format=raw,file=$(BUILD)/disk-p11b.img -serial stdio

# quick smoke gate: smallest wasm guest through engine + WASI
test: test-g1

# Gate runner. The guests under test never exit on their own (login
# prompt, shell, servers), so this always runs to the timeout and the
# assertions read the serial log afterwards. GATE_TIMEOUT is therefore
# the per-gate budget the whole matrix multiplies by, not a real
# duration: at the old hardcoded 600 the 13 RUN_QEMU gates alone cost
# 130 minutes of pure waiting. 180s is far above the real cost of
# reaching a gate marker (KVM ~3s, TCG tens of seconds) and can be
# raised for a slow host: make GATE_TIMEOUT=600 test-all.
# Per-gate marker wait, and it is a HOST-SPEED parameter, not a guest one:
# QEMU_GATE exits as soon as the markers appear, so this only ever costs
# anything when a gate is slow or stuck. The matrix leg that runs pure
# emulation (make test-matrix-tcg) is several times slower than the
# accelerated default on identical guests -- p8a's busy/polite workload did
# not finish inside 180 s there while interleaving perfectly -- so that leg
# reached ~700 s to its final marker. (guests/p8 is sized for an
# accelerated host: the busy guest runs 12M interpreted iterations with a
# yield every 1000. The polite guest was already re-sized for exactly this
# reason -- see the comment in guests/p8/main.go -- and the busy one was
# not.) So the emulated leg gets 900 s per gate rather than a smaller
# workload; marker-watch still exits the moment a gate succeeds.
ifeq ($(KVM_FLAG),-accel tcg)
GATE_TIMEOUT ?= 900
else
GATE_TIMEOUT ?= 180
endif

# QEMU_GATE(<image>, <marker>..., [extra QEMU args]) -- boot an image, stop
# as soon as EVERY marker has appeared on the serial log, then let the
# caller's assertions decide pass/fail.
#
# The guests under test never exit on their own (login prompt, shell,
# servers), so the old runner always ran to its timeout: at 600 s the 13
# plain gates cost 130 minutes of pure waiting, which is what made
# `make test-all` impractical. This polls instead. It also breaks early if
# QEMU dies (image lock, firmware panic) so a broken gate reports in
# seconds rather than after the whole budget, and it ALWAYS exits 0: the
# verdict belongs to the grep block, not to the runner.
define QEMU_GATE_LOG
$(if $(4),$(4),$(BUILD)/serial.log)
endef
define QEMU_GATE
	@rm -f $(call QEMU_GATE_LOG,$(1),$(2),$(3),$(4)) $(BUILD)/qemu.gate.pid
	@env $(QEMU_ENV) $(QEMU) $(QEMU_BASE) -drive format=raw,file=$(1) $(3) -serial file:$(call QEMU_GATE_LOG,$(1),$(2),$(3),$(4)) & \
	 qpid=$$!; echo $$qpid > $(BUILD)/qemu.gate.pid; \
	 trap 'kill $$qpid 2>/dev/null' EXIT INT TERM; \
	 log=$(call QEMU_GATE_LOG,$(1),$(2),$(3),$(4)); \
	 deadline=$$(($$(date +%s) + $(GATE_TIMEOUT))); \
	 while [ $$(date +%s) -lt $$deadline ]; do \
	     ok=1; \
	     for pat in $(2); do grep -qa -- "$$pat" "$$log" 2>/dev/null || ok=0; done; \
	     [ $$ok = 1 ] && break; \
	     kill -0 $$qpid 2>/dev/null || break; \
	     sleep 1; \
	 done; \
	 kill $$qpid 2>/dev/null; wait $$qpid 2>/dev/null; \
	 rm -f $(BUILD)/qemu.gate.pid; \
	 ok=1; for pat in $(2); do grep -qa -- "$$pat" "$$log" 2>/dev/null || ok=0; done; \
	 if [ $$ok = 1 ]; then true; else \
	   echo "GATE FAILED: required marker(s) never appeared in $$log:" >&2; \
	   for pat in $(2); do grep -qa -- "$$pat" "$$log" 2>/dev/null || echo "  missing: $$pat" >&2; done; \
	   exit 1; \
	 fi
endef

# Backwards-compatible alias: callers that pass no markers fall back to
# waiting out the budget.
define RUN_QEMU
	$(call QEMU_GATE,$(1),$(2))
endef

# per-guest wasm gates (Phase 3): each guest prints its marker via fd_write
.PHONY: test-g1 test-g2 test-g3 test-g1-owned test-g2-owned test-g3-owned test-p4 test-p4-owned test-p5a test-p5b test-p7 test-p7-owned test-p8a test-p8-owned test-p8b test-p9 test-p10 test-p11 test-p11b test-p11-gfx test-all

test-g1: VARS := $(BUILD)/vars/g1.fd
test-g1: $(BUILD)/disk-g1.img $(BUILD)/vars/g1.fd
	$(call QEMU_GATE,$(BUILD)/disk-g1.img,'KERNEL-OK' 'hello from C')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log && grep -qa 'hello from C' \
		$(BUILD)/serial.log && echo "TEST PASS (g1)" \
		|| { echo "TEST FAIL (g1)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-g1-owned: VARS := $(BUILD)/vars/g1-owned.fd
test-g1-owned: $(BUILD)/disk-g1-owned.img $(BUILD)/vars/g1-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-g1-owned.img,'KERNEL-OK' 'engine=owned' 'owned backend initialized' 'hello from C')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log \
		&& grep -qa 'engine=owned' $(BUILD)/serial.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial.log \
		&& grep -qa 'hello from C' $(BUILD)/serial.log \
		&& echo "TEST PASS (g1 owned)" \
		|| { echo "TEST FAIL (g1 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-g2-owned: VARS := $(BUILD)/vars/g2-owned.fd
test-g2-owned: $(BUILD)/disk-g2-owned.img $(BUILD)/vars/g2-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-g2-owned.img,'KERNEL-OK' 'engine=owned' 'owned backend initialized' 'hello from Rust')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log \
		&& grep -qa 'engine=owned' $(BUILD)/serial.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial.log \
		&& grep -qa 'hello from Rust' $(BUILD)/serial.log \
		&& echo "TEST PASS (g2 owned)" \
		|| { echo "TEST FAIL (g2 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-g3-owned: VARS := $(BUILD)/vars/g3-owned.fd
test-g3-owned: $(BUILD)/disk-g3-owned.img $(BUILD)/vars/g3-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-g3-owned.img,'KERNEL-OK' 'engine=owned' 'owned backend initialized' 'hello from Go')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log \
		&& grep -qa 'engine=owned' $(BUILD)/serial.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial.log \
		&& grep -qa 'hello from Go' $(BUILD)/serial.log \
		&& echo "TEST PASS (g3 owned)" \
		|| { echo "TEST FAIL (g3 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-g2: VARS := $(BUILD)/vars/g2.fd
test-g2: $(BUILD)/disk-g2.img $(BUILD)/vars/g2.fd
	$(call QEMU_GATE,$(BUILD)/disk-g2.img,'KERNEL-OK' 'hello from Rust')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log && grep -qa 'hello from Rust' \
		$(BUILD)/serial.log && echo "TEST PASS (g2)" \
		|| { echo "TEST FAIL (g2)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-g3: VARS := $(BUILD)/vars/g3.fd
test-g3: $(BUILD)/disk-g3.img $(BUILD)/vars/g3.fd
	$(call QEMU_GATE,$(BUILD)/disk-g3.img,'KERNEL-OK' 'hello from Go')
	@grep -qa 'KERNEL-OK' $(BUILD)/serial.log && grep -qa 'hello from Go' \
		$(BUILD)/serial.log && echo "TEST PASS (g3)" \
		|| { echo "TEST FAIL (g3)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

test-p4: VARS := $(BUILD)/vars/p4.fd
test-p4: $(BUILD)/disk-p4.img $(BUILD)/vars/p4.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p4.img,'rounds ok=3' 'sessions=' '\[kill\] console rc=0')
	@grep -qa 'rounds ok=3' $(BUILD)/serial.log \
		&& grep -qaE 'sessions=[1-9]' $(BUILD)/serial.log \
		&& grep -qa '\[kill\] console rc=0' $(BUILD)/serial.log \
		&& echo "TEST PASS (p4)" \
		|| { echo "TEST FAIL (p4)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-p4-owned: VARS := $(BUILD)/vars/p4-owned.fd
test-p4-owned: $(BUILD)/disk-p4-owned.img $(BUILD)/vars/p4-owned.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p4-owned.img,'rounds ok=3' 'sessions=' '\[kill\] console rc=0' 'owned backend initialized')
	@grep -qa 'rounds ok=3' $(BUILD)/serial.log \
		&& grep -qaE 'sessions=[1-9]' $(BUILD)/serial.log \
		&& grep -qa '\[kill\] console rc=0' $(BUILD)/serial.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial.log \
		&& echo "TEST PASS (p4 owned)" \
		|| { echo "TEST FAIL (p4 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -50; exit 1; }

# Phase 5 gates: BOTH routes must round-trip; u2 must NOT see u1's file
.PHONY: test-p5a test-p5b

test-p5a: VARS := $(BUILD)/vars/p5a.fd
test-p5a: $(BUILD)/disk-p5a.img $(BUILD)/vars/p5a.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p5a.img,'\[p5a\] roundtrip ok')
	@grep -qa '\[p5a\] roundtrip ok' $(BUILD)/serial.log && echo "TEST PASS (p5a)" \
		|| { echo "TEST FAIL (p5a)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-p5b: VARS := $(BUILD)/vars/p5b.fd
test-p5b: $(BUILD)/disk-p5b.img $(BUILD)/vars/p5b.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p5b.img,'\[p5b\] all ok')
	@grep -qa '\[p5b\] all ok' $(BUILD)/serial.log && echo "TEST PASS (p5b)" \
		|| { echo "TEST FAIL (p5b)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }


# Phase 7 gate: scripted serial input drives login -> shell -> cat /etc/motd
.PHONY: test-p7
test-p7: VARS := $(BUILD)/vars/p7.fd
test-p7: $(BUILD)/disk-p7.img $(BUILD)/vars/p7.fd
	bash scripts/run_p7.sh $(BUILD)/serial.log "$(QEMU)" "$(QEMU_ENV)" -- \
		-drive format=raw,file=$(BUILD)/disk-p7.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial.log \
		&& grep -qa 'Welcome to the capability microkernel' $(BUILD)/serial.log \
		&& echo "TEST PASS (p7)" \
		|| { echo "TEST FAIL (p7)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

test-p7-owned: VARS := $(BUILD)/vars/p7-owned.fd
test-p7-owned: $(BUILD)/disk-p7-owned.img $(BUILD)/vars/p7-owned.fd
	bash scripts/run_p7.sh $(BUILD)/serial.log "$(QEMU)" "$(QEMU_ENV)" -- \
		-drive format=raw,file=$(BUILD)/disk-p7-owned.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial.log \
		&& grep -qa 'Welcome to the capability microkernel' $(BUILD)/serial.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial.log \
		&& echo "TEST PASS (p7 owned)" \
		|| { echo "TEST FAIL (p7 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -60; exit 1; }


.PHONY: test-p10
test-p10: VARS := $(BUILD)/vars/p10.fd
test-p10: $(BUILD)/disk-p10.img $(BUILD)/vars/p10.fd
	$(call QEMU_GATE,$(BUILD)/disk-p10.img,'p10a. all ok' 'p10b. all ok')
	@grep -qa 'p10a. all ok' $(BUILD)/serial.log \
	    && grep -qa 'p10b. all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p10 multiuser negatives)" \
	    || { echo "TEST FAIL (p10)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

# Phase 11: VFIO smoke test — PCI enum, BAR map, FB mode, doorbell
test-p11: VARS := $(BUILD)/vars/p11.fd
test-p11: $(BUILD)/disk-p11.img $(BUILD)/vars/p11.fd
	$(call QEMU_GATE,$(BUILD)/disk-p11.img,'p11: pci enum found [1-9][0-9]*' 'p11: all ok',$(P13_PCIE))
	@grep -qa 'p11: pci enum found [1-9][0-9]*' $(BUILD)/serial.log \
	    && grep -qa 'p11: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11 VFIO smoke)" \
	    || { echo "TEST FAIL (p11)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Owned-engine twin of the VFIO smoke gate. The 'owned backend
# initialized' assertion is not decoration: without it a broken image
# would silently pass while testing wasm3.
test-p11-owned: VARS := $(BUILD)/vars/p11-owned.fd
test-p11-owned: $(BUILD)/disk-p11-owned.img $(BUILD)/vars/p11-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-p11-owned.img,'owned backend initialized' 'p11: pci enum found [1-9][0-9]*' 'p11: all ok',$(P13_PCIE))
	@grep -qa 'owned backend initialized' $(BUILD)/serial.log \
	    && grep -qa 'p11: pci enum found [1-9][0-9]*' $(BUILD)/serial.log \
	    && grep -qa 'p11: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11-owned VFIO smoke on the owned engine)" \
	    || { echo "TEST FAIL (p11-owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 11 framebuffer over the owned engine. This gate is what proved the
# graphics TrapOutOfBounds (func=0x519 pc=0x11) was NOT an engine bug:
# vfio_map_bar grew guest memory through sched_runtime_of(), which is null
# for an owned session, so the LFB BAR window sat outside guest memory.
# graphics.wasm then read a garbage {dst,src,len} descriptor and the guest's
# own memory.copy trapped -- the engine's bounds check was right. Now that
# the BAR window is mapped through sched_session_ensure_bytes(), the same
# module renders and the gate is green.
test-p11b-owned: VARS := $(BUILD)/vars/p11b-owned.fd
test-p11b-owned: $(BUILD)/disk-p11b-owned.img $(BUILD)/vars/p11b-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-p11b-owned.img,'owned backend initialized' 'graphics: fb_present ok' 'graphics: lfb readback ok' 'graphics: all ok')
	@grep -qa 'owned backend initialized' $(BUILD)/serial.log \
	    && grep -qa 'graphics: fb_present ok' $(BUILD)/serial.log \
	    && grep -qa 'graphics: lfb readback ok' $(BUILD)/serial.log \
	    && grep -qa 'graphics: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11b-owned graphics on the owned engine)" \
	    || { echo "TEST FAIL (p11b-owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

# Phase 11b: graphics integration — graphics.wasm renders /etc/motd to LFB
test-p11b: VARS := $(BUILD)/vars/p11b.fd
test-p11b: $(BUILD)/disk-p11b.img $(BUILD)/vars/p11b.fd
	$(call QEMU_GATE,$(BUILD)/disk-p11b.img,'graphics: fb_present ok' 'graphics: lfb readback ok' 'graphics: all ok')
	@grep -qa 'graphics: fb_present ok' $(BUILD)/serial.log \
	    && grep -qa 'graphics: lfb readback ok' $(BUILD)/serial.log \
	    && grep -qa 'graphics: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11b graphics integration)" \
	    || { echo "TEST FAIL (p11b)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

 # Phase 11 gfx: legacy-mode disk — graphics.wasm at /vm/app, no init.conf
# gate_mask 0x300, the same as disk-p11b.img: this image had NO /vm/gate at
# all, so the session got the default capability set and fb_set_mode was
# denied for want of CAP_FB. Same guest, same bochs display, same mask --
# the only difference was that p11b shipped the mask and this one did not.
$(BUILD)/disk-p11-gfx.img: $(BUILD)/BOOTX64.EFI build/graphics.wasm | $(BUILD) $(IMG)
	printf '300' > $(BUILD)/gate-p11-gfx.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/graphics.wasm:/vm/app \
	  $(BUILD)/gate-p11-gfx.tmp:/vm/gate

 .PHONY: test-g1 test-g2 test-g3 test-p4 test-p5a test-p5b test-p7 test-p8a test-p8-owned test-p8b test-p9 test-p10 test-p11 test-p11-owned test-p11b test-p11b-owned test-p11-gfx test-p12 test-p12-owned test-p13 test-p13-owned test-p16-owned test-p14 test-all

.PHONY: test-p11-gfx
test-p11-gfx: VARS := $(BUILD)/vars/p11-gfx.fd
test-p11-gfx: $(BUILD)/disk-p11-gfx.img $(BUILD)/vars/p11-gfx.fd
	$(call QEMU_GATE,$(BUILD)/disk-p11-gfx.img,'graphics: all ok')
	@grep -qa 'graphics: all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p11-gfx legacy graphics)" \
	    || { echo "TEST FAIL (p11-gfx)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

 # Phase 12: USB + BT + WLAN services boot as /vm/app with CAP_PCI
# gate_mask 0x1103 = KILL|PORTBIND|DEVMAN|PCI: the usb guest needs
# DevmanEnum to find the xHCI and CAP_PCI to map its BAR. Without them the
# guest reported "no xHCI controller found" with a controller attached.
$(BUILD)/disk-p12.img: $(BUILD)/BOOTX64.EFI build/usb.wasm services/bt/bt.wasm services/wlan/wlan.wasm | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p12.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/usb.wasm:/vm/app \
	  $(BUILD)/gate-p12.tmp:/vm/gate \
	  services/bt/bt.wasm:/boot/modules/bt.wasm \
	  services/wlan/wlan.wasm:/boot/modules/wlan.wasm

$(BUILD)/disk-p12-owned.img: $(BUILD)/BOOTX64.EFI build/usb.wasm services/bt/bt.wasm services/wlan/wlan.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p12.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  build/usb.wasm:/vm/app \
	  $(BUILD)/gate-p12.tmp:/vm/gate \
	  services/bt/bt.wasm:/boot/modules/bt.wasm \
	  services/wlan/wlan.wasm:/boot/modules/wlan.wasm \
	  $(BUILD)/owned.marker:/vm/owned

test-p12-owned: VARS := $(BUILD)/vars/p12-owned.fd
test-p12-owned: $(BUILD)/disk-p12-owned.img $(BUILD)/vars/p12-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-p12-owned.img,'owned backend initialized' 'usb.*all ok' '\[usb\] registers verified [1-9][0-9]* ports',$(P13_PCIE))
	@grep -qa 'owned backend initialized' $(BUILD)/serial.log \
	    && grep -qaE '\[usb\] registers verified [1-9][0-9]* ports' $(BUILD)/serial.log \
	    && echo "TEST PASS (p12-owned usb+bt+wlan on the owned engine)" \
	    || { echo "TEST FAIL (p12-owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

 .PHONY: test-p12
 test-p12: VARS := $(BUILD)/vars/p12.fd
 test-p12: $(BUILD)/disk-p12.img $(BUILD)/vars/p12.fd
	$(call QEMU_GATE,$(BUILD)/disk-p12.img,'usb.*all ok' '\[usb\] registers verified [1-9][0-9]* ports',$(P13_PCIE))
	@grep -qa 'e1000.*all ok\|usb.*all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p12 usb+bt+wlan)" \
	    || { echo "TEST FAIL (p12)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

 # Phase 13: E1000 + AHCI VFIO drivers boot as /vm/app and /vm/app2 (legacy mode)
# gate_mask 0x1103 = KILL|PORTBIND|DEVMAN|PCI. DevmanEnum checks
# CAP_DEVMAN, which is a different bit from CAP_PCI; without it the
# denial is audited as "op=ENUM reason=cap" and both guests report
# "device not found" no matter what hardware is attached.
$(BUILD)/disk-p13.img: $(BUILD)/BOOTX64.EFI services/e1000/e1000.wasm services/ahci/ahci.wasm | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p13.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/e1000/e1000.wasm:/vm/app \
	  services/ahci/ahci.wasm:/vm/app2 \
	  $(BUILD)/gate-p13.tmp:/vm/gate

$(BUILD)/disk-p13-owned.img: $(BUILD)/BOOTX64.EFI services/e1000/e1000.wasm services/ahci/ahci.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf '1103' > $(BUILD)/gate-p13.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/e1000/e1000.wasm:/vm/app \
	  services/ahci/ahci.wasm:/vm/app2 \
	  $(BUILD)/gate-p13.tmp:/vm/gate \
	  $(BUILD)/owned.marker:/vm/owned

test-p13-owned: VARS := $(BUILD)/vars/p13-owned.fd
test-p13-owned: $(BUILD)/disk-p13-owned.img $(BUILD)/vars/p13-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-p13-owned.img,'owned backend initialized' 'e1000.*all ok' 'ahci: vs=[0-9a-f][0-9a-f][0-9a-f][0-9a-f]' 'ahci.*all ok',$(P13_PCIE))
	@grep -qa 'owned backend initialized' $(BUILD)/serial.log \
	    && grep -qa 'dma unavailable' $(BUILD)/serial.log \
	    && grep -qa 'no IOMMU on this platform' $(BUILD)/serial.log \
	    && grep -qa 'op=DMA_MAP reason=noiommu' $(BUILD)/serial.log \
	    && grep -qaE 'e1000: (mac verified|registers accessible)' $(BUILD)/serial.log \
	    && grep -qaE 'ahci: vs=[0-9a-f]{8}' $(BUILD)/serial.log \
	    && grep -qa 'e1000.*all ok' $(BUILD)/serial.log \
	    && grep -qa 'ahci.*all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p13-owned e1000+ahci on the owned engine)" \
	    || { echo "TEST FAIL (p13-owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

.PHONY: test-p13
test-p13: VARS := $(BUILD)/vars/p13.fd
test-p13: $(BUILD)/disk-p13.img $(BUILD)/vars/p13.fd
	$(call QEMU_GATE,$(BUILD)/disk-p13.img,'e1000.*all ok' 'ahci: vs=[0-9a-f][0-9a-f][0-9a-f][0-9a-f]' 'ahci.*all ok',$(P13_PCIE))
	@grep -qa 'dma unavailable' $(BUILD)/serial.log \
	    && grep -qa 'no IOMMU on this platform' $(BUILD)/serial.log \
	    && grep -qa 'op=DMA_MAP reason=noiommu' $(BUILD)/serial.log \
	    && grep -qa 'e1000.*all ok' $(BUILD)/serial.log \
	    && grep -qaE 'e1000: (mac verified|registers accessible)' $(BUILD)/serial.log \
	    && grep -qaE 'ahci: vs=[0-9a-f]{8}' $(BUILD)/serial.log \
	    && grep -qa 'ahci.*all ok' $(BUILD)/serial.log \
	    && echo "TEST PASS (p13 e1000+ahci)" \
	    || { echo "TEST FAIL (p13)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

.PHONY: test-p9
test-p9: VARS := $(BUILD)/vars/p9.fd
test-p9: $(BUILD)/disk-p9.img $(BUILD)/vars/p9.fd
	bash scripts/run_p9.sh $(BUILD)/serial-p9.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p9.img $(QEMU_BASE)
	@grep -qa 'p9. udp ok' $(BUILD)/serial-p9.log \
	    && grep -qa 'p9. http ok' $(BUILD)/serial-p9.log \
	    && grep -qa 'p9. external ok' $(BUILD)/serial-p9.log \
	    && grep -qaE 'p9. ping ok rtt=[0-9]+ len=16' $(BUILD)/serial-p9.log \
	    && grep -qaE 'net. dhcp lease 10\.0\.2\.15 xid=0x[0-9a-f]{8} mask 255\.255\.255\.0 router 10\.0\.2\.2' $(BUILD)/serial-p9.log \
	    && echo "TEST PASS (p9 network E2E)" \
	    || { echo "TEST FAIL (p9)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p9.log | tail -40; exit 1; }

 # Phase 8: cooperative multitasking — both sessions make progress
$(BUILD)/preempt_busy.raw: guests/preempt_busy.wat | $(BUILD)
	$(WAT2WASM) $< -o $@
$(BUILD)/preempt_busy.wasm: $(BUILD)/preempt_busy.raw
	python3 scripts/add_abiver.py $< $@ 2

$(BUILD)/disk-preempt-owned.img: $(BUILD)/BOOTX64.EFI $(BUILD)/preempt_busy.wasm build/hello1.wasm $(BUILD)/owned.marker | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  $(BUILD)/preempt_busy.wasm:/vm/app build/hello1.wasm:/vm/app2 \
	  $(BUILD)/owned.marker:/vm/owned

# Phase 8 persistence image: guests/p8b at /vm/app, on a RAW virtio-blk
# volume with no filesystem.
#
# gate_mask: CAP_FS_ADMIN is section 7 bit 4 = 0x10. It is REQUIRED, not
# cosmetic: ABI section 3 F31 makes kern_blk_read/write whole-disk raw
# access, and the kernel audits and returns -1 for any session without the
# bit. This is the same missing-capability-bit class as p11's DEVMAN and
# p11-gfx's CAP_FB, both of which cost a rebuild cycle each -- so the bit is
# set here and the guest's own failure line names it if it is ever wrong.
$(BUILD)/disk-p8b.img: $(BUILD)/BOOTX64.EFI build/p8b.wasm | $(BUILD) $(IMG)
	printf '10' > $(BUILD)/gate-p8b.tmp
	$(IMG) -o $@ -size 8M -efi $(BUILD)/BOOTX64.EFI \
	  build/p8b.wasm:/vm/app \
	  $(BUILD)/gate-p8b.tmp:/vm/gate

$(BUILD)/disk-p8.img: $(BUILD)/BOOTX64.EFI build/test_p8.wasm services/fs/fs.wasm services/console/console.wasm services/login/login.wasm | $(BUILD) $(IMG)
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	  build/test_p8.wasm:/vm/app \
	  build/test_p8.wasm:/vm/app2

# The two async-preemption gates run guests/p8, whose busy guest does 12M
# iterations. Under KVM it finishes at 512 MiB; under TCG (software
# emulation) the same guest managed ~1.25M of 12M iterations in the 900 s
# window at 512 MiB and passed at 1024. A harness accommodation for the
# emulator, not a behaviour change -- these two gates get the old default.
test-p8a: QEMU_MEM := 1024
test-p8a: VARS := $(BUILD)/vars/p8a.fd
test-p8a: $(BUILD)/disk-p8.img $(BUILD)/vars/p8a.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p8.img,'busy. done marks=' 'polite. done ticks=')
	@grep -qa 'busy. done marks=' $(BUILD)/serial.log \
		&& grep -qa 'polite. done ticks=' $(BUILD)/serial.log \
		&& echo "TEST PASS (p8a no-starvation)" \
		|| { echo "TEST FAIL (p8a)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

.PHONY: test-p8-owned
test-p8-owned: QEMU_MEM := 1024
test-p8-owned: VARS := $(BUILD)/vars/p8-owned.fd
test-p8-owned: $(BUILD)/disk-preempt-owned.img $(BUILD)/vars/p8-owned.fd
	@rm -f $(BUILD)/serial-p8-owned.log
	$(call QEMU_GATE,$(BUILD)/disk-preempt-owned.img,'busy-start' 'hello from C' 'owned backend initialized',,$(BUILD)/serial-p8-owned.log)
	@grep -qa 'busy-start' $(BUILD)/serial-p8-owned.log \
		&& grep -qa 'hello from C' $(BUILD)/serial-p8-owned.log \
		&& grep -qa 'owned backend initialized' $(BUILD)/serial-p8-owned.log \
		&& echo "TEST PASS (p8 owned async preemption)" \
		|| { echo "TEST FAIL (p8 owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p8-owned.log | tail -40; exit 1; }

# Phase 8b: persistence — write via virtio-blk, reset, read back
$(BUILD)/persist.img:
	dd if=/dev/zero of=$@ bs=1M count=0 seek=64 status=none

# Extra drives for the p8b persistence gate. Kept in a variable because
# $(call) splits on commas and -drive args are full of them, and it must
# stay on ONE line: a newline inside an expansion splits the recipe.
P8B_DRIVES := -drive id=p8b,format=raw,file=$(BUILD)/persist.img,if=none -device virtio-blk-pci,drive=p8b

# QEMU args containing commas must reach $(call ...) through a variable:
# $(call) splits its argument list on commas, so an inline
# "-netdev user,id=n1" silently loses everything after the first comma.
NETDEV_ARGS := -netdev user,id=n1 -device virtio-net-pci,netdev=n1

test-p8b: VARS := $(BUILD)/vars/p8b.fd
test-p8b: $(BUILD)/disk-p8b.img $(BUILD)/vars/p8b.fd $(BUILD)/persist.img
	@rm -f $(BUILD)/serial.log $(BUILD)/persist.img
	@dd if=/dev/zero of=$(BUILD)/persist.img bs=1M count=0 seek=64 status=none
	@echo "boot 1/2: expect the probe to WRITE the pattern"
	$(call QEMU_GATE,$(BUILD)/disk-p8b.img,'virtio-blk. ready sectors=0x0*[1-9a-f]' 'p8b. wrote at lba=8',$(P8B_DRIVES))
	@echo "boot 2/2: same persist.img, expect the pattern to come back"
	$(call QEMU_GATE,$(BUILD)/disk-p8b.img,'p8b. readback ok at lba=8',$(P8B_DRIVES))
	@echo "TEST PASS (p8b persistence: wrote, rebooted, read back)"\
	    || { echo "TEST FAIL (p8b persistence)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -20; exit 1; }

# Phase 10: host-mode unit tests for service logic
# Static checks that the standard gate was missing. `services/net` stayed
# red under `go test -race` for two Phase-16 tests for a long time, and the
# cause was a copylocks bug (value receivers on a struct embedding a
# sync.Mutex) in guests/lib -- which `go vet` reports statically and without
# needing -race. A bug that survived because nothing ran the check should not
# survive twice, so the check is now in the gate.
#
# Two known findings are NOT in the vetted set, and are named here rather than
# silently skipped:
#   - guests/lib/mediatedbar.go: "possible misuse of unsafe.Pointer". This is
#     the intentional guest-linear-memory window pattern (a kernel-returned
#     offset converted to a slice), the same shape as ptrAt in services/net.
#     It is a deliberate use of unsafe, not a defect to remove.
#   - services/display/terminal/term.go: self-assignment of t.mouseMode.
#     Harmless, but it belongs to the other lane's file and is recorded in
#     MEMORY.md for them to fix.
# Concurrency coverage in the standard gate. `services/net` was red under
# `-race` for two Phase-16 tests and the cause was a copylocks bug in
# guests/lib; nothing in `test-all` ran `-race`, so the bug had a home for a
# long time. A race that only fires on some runs is still a race, and
# test-vet does not catch all of them, so this gate exists alongside it.
#
# KNOWN EXCLUSION, named rather than hidden:
# services/login and services/shell were BOTH excluded here while they had
# races: login's intermittent AddSession write, and shell's chcaps polling
# reading a locked field. Both are now fixed (see MEMORY.md LOGIN-RACE-FIXED
# and SHELL-CHCAPS-RACE), verified by repeated -race loops sized to finish,
# and both are back in the vetted set. There is no exclusion any more.
test-race:
	@fail=0; \
	 for d in guests/lib services/net services/fs services/console \
	          services/login services/pkg services/shell \
	          services/display/terminal; do \
	   out=$$(cd $$d && go test -count=1 -race . 2>&1); \
	   if [ $$? != 0 ]; then echo "TEST FAIL (race) $$d:"; echo "$$out" | tail -20 | sed 's/^/  /'; fail=1; fi; \
	 done; \
	 if [ $$fail = 0 ]; then echo "TEST PASS (race)"; else exit 1; fi

# Catch a class of staleness that make structurally cannot: every disk-*.img
# rule writes its own configuration with `printf` INSIDE the recipe (the
# init.conf, the /etc/users, the capability gate mask, the motd). Editing any
# of that content changes the recipe, and a recipe change is not a
# prerequisite change -- so make happily reuses the old image and the gate
# asserts yesterday's configuration. I hit this twice today: a CAP_FB gate
# mask was added to a recipe and the image did not rebuild, and the denial
# persisted until `rm -f` was issued by hand.
#
# This target cannot fix that (hoisting every printf into its own prerequisite
# is a refactor of ~20 rules), but it can make the condition visible.
#
# It is a DIAGNOSTIC, deliberately not in `test-all`, and the first version of
# it was wrong in a way worth recording: I wired it into test-all and it
# failed immediately on 4 images -- legitimately, since they were built from
# older recipes. But it would fire on EVERY Makefile edit, including ones
# that change nothing about any image (adding a gate, fixing a pattern). A
# check that is red during ordinary work is a check people learn to ignore,
# which is worse than no check. So: run it when you suspect staleness, and
# act on the STALE lines it prints.
#
#   make check-stale-images
# Does any gate actually drive a built-in, or does one merely exist?
# test-p17 and test-p18 shipped in `test-all` certifying Phase 17 and 18 while
# typing nothing at all, and nothing in the harness could see it: the built-in
# list lived only in the shell's dispatcher and the typed commands only in the
# gate scripts, so "a command exists" and "something runs it" were never
# compared. This reports the difference.
#
# `--check` fails only when a NEWLY added built-in is undriven, tracked
# against verify/cmd-coverage.baseline. A plain count would be unusable
# noise -- vi, ssh and reboot are genuinely hard to drive and may never be --
# so the invariant is "coverage must not regress", not "everything is
# covered". Run without --check for the full report.
# Guard against losing targets by accident. This exists because a single
# commit of mine deleted 1017 lines of this file: an edit script's fallback
# branch rebuilt the file from a stale split, the result looked plausible,
# `git status` was clean because I committed the damage, and the only check
# I ran afterwards did not read the Makefile at all. The whole `test-all`
# gate set was gone and the suite still reported success on what remained.
#
# A line floor is the blunt instrument that catches exactly that: a large
# single-commit deletion is never intentional. Lower MAKEFILE_MIN_LINES only
# when targets are really removed.
# Is every gate marker DECLARED with the property it is supposed to prove?
#
# p12 asserted a pattern that could never match; p8b was labelled
# "persistence" while grepping a device-probe line; p17 and p18 asserted
# only bring-up strings while typing nothing. All four shipped green. What
# they share is that no document said what any marker was supposed to prove,
# so "could a not-found path print this?" was never asked. verify/GATES.md
# declares every marker with a class (boot / guest / hw) and the property it
# establishes; this fails when the Makefile uses a marker nobody declared.
# Does each built image contain the CONFIG its recipe says it should?
#
# Every disk-*.img rule writes its config (init.conf, the capability gate
# mask, /etc/users, the motd) with printf INSIDE the recipe. A recipe change
# is not a prerequisite change, so editing any of it leaves a stale image and
# make reuses it -- three times today this bit me, twice as a capability
# denial that "should have been fixed". check-stale-images catches that by
# mtime but had to be demoted to a diagnostic because it fires on every
# Makefile edit. CONTENT does not have that problem: an edit that changes
# nothing about an image cannot change what is inside it.
#
# Expectations are recorded per (image, path) under verify/image-config/ and
# regenerated deliberately with --update after a legitimate change. Starts
# with the capability masks -- where both missing-capability bugs lived --
# and the per-phase init.confs.
check-image-config:
	@bash scripts/check-image-config.sh

check-gate-manifest:
	@bash scripts/check-gate-manifest.sh

check-makefile-intact:
	@lines=$$(wc -l < Makefile); \
	 if [ "$$lines" -lt "$(MAKEFILE_MIN_LINES)" ]; then \
	   echo "TEST FAIL (makefile): $$lines lines, floor is $(MAKEFILE_MIN_LINES) -- targets were lost"; \
	   echo "  if that removal was intended, lower MAKEFILE_MIN_LINES deliberately"; \
	   exit 1; \
	 fi; \
	 echo "TEST PASS (makefile): $$lines lines (floor $(MAKEFILE_MIN_LINES))"

check-cmd-coverage:
	@bash scripts/check-cmd-coverage.sh --check

# Decides whether every committed services/*/*.wasm matches a fresh build from
# its own sources. Go wasm builds are byte-reproducible here, so this is a hash
# comparison, not an mtime guess. It is a gate because three separate wrong
# results in one session traced to a stale artifact -- including a disk image
# rebuilt from a reused .raw intermediate, which made a guest look like it was
# printing success from a denied path. --quick only rebuilds modules whose
# sources are newer, and is for inner-loop use, not for the gate.
check-artifact-freshness:
	@bash scripts/check-artifact-freshness.sh

cmd-coverage-report:
	@bash scripts/check-cmd-coverage.sh

check-stale-images:
	@stale=0; \
	 for img in $(BUILD)/disk-*.img; do \
	   [ -e "$$img" ] || continue; \
	   if [ "$$img" -ot Makefile ]; then \
	     echo "STALE: $$img is older than Makefile -- built from an older recipe"; stale=1; \
	   fi; \
	 done; \
	 if [ $$stale = 0 ]; then echo "TEST PASS (artifacts fresh)"; \
	 else echo "TEST FAIL (artifacts): rebuild with 'rm -f build/disk-*.img', or 'make image'"; exit 1; fi

test-vet:
	@fail=0; \
	 for d in services/net services/shell services/fs services/console \
	          services/login services/pkg; do \
	   out=$$(cd $$d && go vet ./... 2>&1); \
	   if [ -n "$$out" ]; then echo "TEST FAIL (vet) $$d:"; echo "$$out" | sed 's/^/  /'; fail=1; fi; \
	 done; \
	 if [ $$fail = 0 ]; then echo "TEST PASS (vet)"; else exit 1; fi

test-unit:
	cd services/fs && go test -v -count=1 .
	cd services/login && go test -count=1 .
	cd services/console && go test -count=1 .
	cd guests/lib && go test -count=1 .

# ---- gofmt (practice: formatting is gated, not optional) ----
# First-party Go only: build outputs, the vendored engine, the lane
# worktrees and the node_modules/.kilo trees are not ours to format.
GO_SRC_FIND := find . -name '*.go' -not -path './build/*' -not -path './.git/*' \
	-not -path '*/node_modules/*' -not -path './.kilo/*' \
	-not -path './third_party/*' -print0

.PHONY: fmt test-fmt

fmt:
	@$(GO_SRC_FIND) | xargs -0 -r gofmt -w
	@echo "TEST PASS (fmt applied)"

# In test-all: gofmt is semantics-preserving, so there is never a reason
# to defer a formatting diff, and an ungated tree lets formatting rot
# until it is mixed into a behavioural change. Use the toolchain the
# modules pin (go 1.24) so doc-comment reformatting does not ping-pong.
test-fmt:
	@command -v gofmt >/dev/null 2>&1 || { echo "TEST FAIL (fmt): gofmt not found -- this gate would pass vacuously"; exit 1; }; \
	n=$$($(GO_SRC_FIND) | tr -cd '\0' | wc -c); \
	if [ "$$n" -lt 10 ]; then echo "TEST FAIL (fmt): only $$n Go files found; the gate would pass vacuously"; exit 1; fi; \
	bad=$$($(GO_SRC_FIND) | xargs -0 -r gofmt -l); \
	if [ -n "$$bad" ]; then \
	    echo "TEST FAIL (fmt): unformatted Go files:"; \
	    echo "$$bad" | sed 's/^/  /'; \
	    echo "  fix with: make fmt"; \
	    exit 1; \
	fi; \
	echo "TEST PASS (fmt)"

# The jitc development oracle (AGENTS: "services/jitc (host/cmd) — engine
# oracle; not a production engine"). Its tests decode the real service
# corpus and diff native-vs-interpreter, so they are the canary for the
# owned engine's semantics -- and nothing ran them, which is how
# TestDiffRealCoverage sat wedged past 11 minutes unnoticed. It needs no
# QEMU and takes a few seconds, and test-all already needs a Go toolchain
# (test-unit), so it runs in the aggregate.
.PHONY: test-oracle
# The oracle's differential sweep and the dump/count tests read the real
# service corpus, so those artifacts are build dependencies of the gate --
# otherwise `make test-oracle` on a clean tree fails on missing files
# instead of testing anything.
ORACLE_CORPUS := services/fs/fs.wasm.raw services/shell/shell.wasm.raw \
	services/login/login.wasm.raw services/console/console.wasm.raw \
	services/net/net.wasm.raw

test-oracle: $(ORACLE_CORPUS)
	cd services/jitc && go vet ./... && go test -count=1 -timeout 600s ./...

# kernel-substrate unit tests (practice #2 regression infra): real ports/
# kernsvc/fsroute/devblk/input objects against a fake scheduler on host.
# -iquote (not -I) for the kernel headers: plain -I puts core/ ahead of
# the system dirs, so libc's <sched.h> resolves to the kernel's
# core/sched.h and <pthread.h> then fails on cpu_set_t. Kernel sources
# include their own headers as "x.h", which -iquote still resolves.
HT_CXXFLAGS := -std=c++20 -O1 -g -Wall -pthread -iquote core -iquote arch/x86_64
HT_OBJS := $(BUILD)/ht/ports.o $(BUILD)/ht/kernsvc.o $(BUILD)/ht/fsroute.o \
           $(BUILD)/ht/devblk.o $(BUILD)/ht/input.o $(BUILD)/ht/vfio_hoststub.o \
           $(BUILD)/ht/preempt.o $(BUILD)/ht/lock_host.o $(BUILD)/ht/log.o \
           $(BUILD)/ht/cap_table.o $(BUILD)/ht/perf.o $(BUILD)/ht/sha256.o


$(BUILD)/hosttest: tools/hosttest.cc $(HT_OBJS) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $(BUILD)/ht/hosttest.o
	$(CXX) -no-pie -pthread $(BUILD)/ht/hosttest.o $(HT_OBJS) -o $@

$(BUILD)/ht/%.o: core/%.cc $(wildcard core/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -DHOST_BUILD -c $< -o $@

# preempt.o programs the PIT via port I/O (outb) — a privileged
# instruction that SIGSEGVs in userspace. HOST_BUILD compiles that path
# out (divisor math + quantum state stay live for the T24 gate).
$(BUILD)/ht/preempt.o: core/preempt.cc $(wildcard core/*.h) | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -DHOST_BUILD -c $< -o $@

$(BUILD)/ht/vfio_hoststub.o: core/vfio_hoststub.cc core/vfio.h | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $@

# Host-side lock impl (lives in arch/ but compiled with HT_CXXFLAGS,
# not the kernel -ffreestanding build). The host test runs on Linux
# where `cli` would segfault; arch/x86_64/lock_host.cc uses C11
# atomics + a no-op IRQ save instead.
$(BUILD)/ht/lock_host.o: arch/x86_64/lock_host.cc core/arch_lock.h | $(BUILD)
	@mkdir -p $(dir $@)
	$(CXX) $(HT_CXXFLAGS) -c $< -o $@

test-kernel: $(BUILD)/hosttest $(BUILD)/rt_heap_test
	$(BUILD)/hosttest
	$(BUILD)/rt_heap_test

# Freestanding heap regression (core/rt.cc over a fake bump pool):
# large blocks must recycle. mm_alloc cannot free, so a leaked block is
# pool memory gone forever -- which is how six services exhausted the
# 447 MiB pool and killed a guest with Go's "out of memory".
$(BUILD)/rt_heap_test: tools/rt_heap_test.cc core/rt.cc | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -iquote core \
	    tools/rt_heap_test.cc core/rt.cc -o $@

.PHONY: test-wasm-interp

test-wasm-interp: | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/wasm_interp_test.cc core/wasm/wasm.cc -o $(BUILD)/wasm_interp_test
	$(BUILD)/wasm_interp_test

.PHONY: test-ctx-fpu

# FPU/SSE state must survive a session switch. ctx_switch used to preserve
# only the callee-saved GPRs, which is legal for an ordinary call but not
# for a switch: the scheduler switches out mid-interpreter, and wasm guests
# use floats (graphics.wasm alpha blends). Drives the real arch ctx.S
# through two contexts; against the pre-fix asm all 16 xmm registers and
# MXCSR mismatch.
test-ctx-fpu: | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/ctx_fpu_test.cc core/ctx.cc arch/x86_64/ctx.S -o $(BUILD)/ctx_fpu_test
	$(BUILD)/ctx_fpu_test

.PHONY: test-wasm-artifacts

# build/hello1.wasm is produced by the g1 disk rule; declaring it here
# turns "someone cleaned build/" into a rebuild instead of a hard error.
test-wasm-artifacts: | $(BUILD) build/hello1.wasm $(wildcard services/*/*.wasm)
	$(CXX) -std=c++20 -O2 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/wasm_artifact_test.cc core/wasm/owned_session.cc core/wasm/wasm.cc -o $(BUILD)/wasm_artifact_test
	$(BUILD)/wasm_artifact_test $(wildcard services/*/*.wasm) build/hello1.wasm

.PHONY: test-wasm-fuzz

test-wasm-fuzz: | $(BUILD)
	$(CXX) -std=c++20 -O2 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. tools/wasm_fuzz_test.cc core/wasm/wasm.cc -o $(BUILD)/wasm_fuzz_test
	$(BUILD)/wasm_fuzz_test 30

.PHONY: test-wasm-owned

test-wasm-owned: | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/wasm_owned_session_test.cc core/wasm/owned_session.cc core/wasm/wasm.cc -o $(BUILD)/wasm_owned_session_test
	$(BUILD)/wasm_owned_session_test

.PHONY: test-wasm-wasi

test-wasm-wasi: | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/wasm_wasi_bridge_test.cc core/wasm/wasi_bridge.cc core/wasm/owned_session.cc core/wasm/wasm.cc -o $(BUILD)/wasm_wasi_bridge_test
	$(BUILD)/wasm_wasi_bridge_test

.PHONY: test-wasm-wasi-artifacts

test-wasm-wasi-artifacts: build/hello1.wasm build/hello2.wasm build/hello3.wasm | $(BUILD)
	$(CXX) -std=c++20 -O1 -g -Wall -Wextra -Werror -fno-exceptions -fno-rtti -I. -Icore tools/wasm_wasi_bridge_test.cc core/wasm/wasi_bridge.cc core/wasm/owned_session.cc core/wasm/wasm.cc -o $(BUILD)/wasm_wasi_bridge_test
	$(BUILD)/wasm_wasi_bridge_test build/hello1.wasm build/hello2.wasm build/hello3.wasm

# Phase 8.2: AP bring-up gate. Boots with -smp 4 and asserts each
# AP booted on serial. The MADT parser reads the SMP count from the
# ACPI tables; ap_boot sends SIPI to each AP. If the trampoline or
# per-CPU scheduler state is broken, the [ap] cpuN booted lines
# won't appear and the gate fails.
test-p14: VARS := $(BUILD)/vars/p14.fd
test-p14: $(BUILD)/disk-p14.img $(BUILD)/vars/p14.fd
	@rm -f $(BUILD)/serial.log
	$(call QEMU_GATE,$(BUILD)/disk-p14.img,'KERNEL-OK',-smp 4)
	# Two independent assertions:
	#  (a) 4 intact "[ap] cpuN booted" lines. Before the console lock
	#      (arch/x86_64/uart.cc) two cores could interleave inside one
	#      line ("[acpup2]  bcoooted") on a boot that was correct, so
	#      bring-up was proven by marker COUNT.
	#  (b) >=4 "booted" markers + the AP ack count, so a partial
	#      bring-up still fails even if (a) is satisfied by oddities.
	@boots=$$(grep -ao 'booted' $(BUILD)/serial.log | wc -l); \
	 intact=$$(grep -ac '\[ap\] cpu[0-9] booted' $(BUILD)/serial.log); \
	 if [ "$$intact" -eq 4 ] && [ "$$boots" -ge 4 ] \
	    && grep -aqE '(^|[^0-9a-f])0*3 APs acked' $(BUILD)/serial.log \
	    && grep -qa 'APs parked: sessions run on core 0' $(BUILD)/serial.log \
	    && grep -qa 'KERNEL-OK' $(BUILD)/serial.log; then \
	    echo "TEST PASS (p14 AP bring-up, cores=4, serial not interleaved; APs parked, NOT multi-core sessions)"; \
	 else \
	    echo "TEST FAIL (p14) booted_markers=$$boots intact_lines=$$intact (missing the 'APs parked' line means sched_run_ap regressed)"; \
	    sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; \
	 fi

$(BUILD)/disk-p14.img: $(BUILD)/BOOTX64.EFI $(BUILD)/disk-p7.img
	@mkdir -p $(dir $@)
	cp $(BUILD)/disk-p7.img $@

# Phase 14 (userland arc): shell pipes + built-ins over serial.
# test-p14 is the AP bring-up gate (it passes -smp 4 through
# QEMU_GATE's extra-args slot); test-p14sh is the shell
# userland gate (QEMU_BASE, 1 vCPU): cp motd, pipe chain
# cat|grep|sort|head, sleep, date — output must reach serial.
# scripts/run_p14.sh drives the session; asserts below check the log.
.PHONY: test-p14sh
test-p14sh: VARS := $(BUILD)/vars/p14sh.fd
test-p14sh: $(BUILD)/disk-p7.img $(BUILD)/vars/p14sh.fd
	bash scripts/run_p14.sh $(BUILD)/serial-p14sh.log "$(QEMU)" "$(QEMU_ENV)" -- \
		-drive format=raw,file=$(BUILD)/disk-p7.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial-p14sh.log \
		&& grep -qa 'Welcome to the capability microkernel' $(BUILD)/serial-p14sh.log \
		&& grep -qa 'UTC' $(BUILD)/serial-p14sh.log \
		&& grep -qa 'stat-cov-ok-9941' $(BUILD)/serial-p14sh.log \
		&& grep -qa 'whoami-cov-ok-9942' $(BUILD)/serial-p14sh.log \
		&& grep -qa 'built-ins:' $(BUILD)/serial-p14sh.log \
		&& echo "TEST PASS (p14sh shell pipes + coverage)" \
		|| { echo "TEST FAIL (p14sh)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p14sh.log | tail -40; exit 1; }

# Phase 15: identity/auth/observability over serial (boot shell, uid 0).
# top shows sessions; kill-session without CAP_KILL is denied AND audited;
# dmesg/audit read the denial back via LOGDUMP; passwd rotates a hash.
# (login old-reject/new-accept is proven in services/login host tests
# against the real AUTH path; serial login scripting is out of scope.)
.PHONY: test-p15
test-p15: VARS := $(BUILD)/vars/p15.fd
test-p15: $(BUILD)/disk-p7.img $(BUILD)/vars/p15.fd
	bash scripts/run_p15.sh $(BUILD)/serial-p15.log "$(QEMU)" "$(QEMU_ENV)" -- \
		-drive format=raw,file=$(BUILD)/disk-p7.img $(QEMU_BASE)
	@grep -qa 'sessions live' $(BUILD)/serial-p15.log \
		&& grep -qaE 'kill-session: denied' $(BUILD)/serial-p15.log \
		&& grep -qa 'op=KILL' $(BUILD)/serial-p15.log \
		&& grep -qa 'passwd: ok' $(BUILD)/serial-p15.log \
		&& grep -qa 'pool total=' $(BUILD)/serial-p15.log \
		&& echo "TEST PASS (p15 identity/observability)" \
		|| { echo "TEST FAIL (p15)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p15.log | tail -40; exit 1; }

 # Phase 16 disk: net stack + shell built-ins (ping/nc/http/netstat/ipaddr/ssh)
$(BUILD)/disk-p16.img: $(BUILD)/BOOTX64.EFI \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm services/init/init.wasm \
                       services/shell/shell.wasm services/net/net.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nnet net.wasm 0x1022\nlogin login.wasm 0x1008\nshell shell.wasm 0x1008\n' > $(BUILD)/init-p16.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm services/net/net.wasm:/boot/modules/net.wasm \
	  $(BUILD)/init-p16.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

$(BUILD)/disk-p16-owned.img: $(BUILD)/BOOTX64.EFI \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm services/init/init.wasm \
                       services/shell/shell.wasm services/net/net.wasm \
                       $(BUILD)/etc_users.txt $(BUILD)/owned.marker | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nnet net.wasm 0x1022\nlogin login.wasm 0x1008\nshell shell.wasm 0x1008\n' > $(BUILD)/init-p16-owned.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm services/net/net.wasm:/boot/modules/net.wasm \
	  $(BUILD)/init-p16-owned.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users \
	  $(BUILD)/owned.marker:/vm/owned

  .PHONY: test-p16
  test-p16: VARS := $(BUILD)/vars/p16.fd
  test-p16: $(BUILD)/disk-p16.img $(BUILD)/vars/p16.fd
	bash scripts/run_p16.sh $(BUILD)/serial-p16.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p16.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial-p16.log \
		&& grep -qa '\[net\] up' $(BUILD)/serial-p16.log \
		&& grep -qaE 'net. dhcp lease 10\.0\.2\.15 xid=0x[0-9a-f]{8} mask 255\.255\.255\.0 router 10\.0\.2\.2' $(BUILD)/serial-p16.log \
		&& grep -qaE '64 bytes from 10\.0\.2\.2: icmp_seq=[0-9]+ ttl=64 time=[0-9]+ms' $(BUILD)/serial-p16.log \
		&& grep -qa '\[net\] serving' $(BUILD)/serial-p16.log \
		&& grep -qa 'dhcp: renew ok' $(BUILD)/serial-p16.log \
		&& grep -qa 'usage: ssh' $(BUILD)/serial-p16.log \
		&& grep -qa 'nc: UDP 10.0.2.2:5599' $(BUILD)/serial-p16.log \
		&& grep -qa 'HTTP/1.0 200' $(BUILD)/serial-p16.log \
		&& echo "TEST PASS (p16 net client userland + dhcp renew + ssh builtin)" \
		|| { echo "TEST FAIL (p16)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p16.log | tail -40; exit 1; }

# Phase 16 network client userland over the owned engine. This gate is what
# forced the device windows off the wasm3 API: netwin_attach_impl took an
# IM3Runtime and called m3_GetMemory/ResizeMemory, and an owned session's
# sched_runtime_of() returns null, so net.wasm silently got no RX/TX windows
# and exited. Windows now attach by session id through the backend-neutral
# sched_session_memory()/sched_session_ensure_bytes(), and the guest's frozen
# 64 MiB window placement is reachable because ensure_memory_bytes lifts the
# per-session bound instead of refusing. Needs QEMU_MEM=1024: see the IR RAM
# budget note on QEMU_BASE.
  test-p16-owned: VARS := $(BUILD)/vars/p16-owned.fd
  test-p16-owned: $(BUILD)/disk-p16-owned.img $(BUILD)/vars/p16-owned.fd
	$(call QEMU_GATE,$(BUILD)/disk-p16-owned.img,'owned backend initialized' 'shell ready' '\[net\] up' '\[net\] serving',$(NETDEV_ARGS))
	@grep -qa 'owned backend initialized' $(BUILD)/serial.log \
	    && grep -qa 'shell ready' $(BUILD)/serial.log \
	    && grep -qa '\[net\] up' $(BUILD)/serial.log \
	    && grep -qa '\[net\] serving' $(BUILD)/serial.log \
	    && echo "TEST PASS (p16-owned net client userland on the owned engine)" \
	    || { echo "TEST FAIL (p16-owned)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -40; exit 1; }

 # Phase 17 disk: capability & port introspection (ports/sessinfo/caphint/chcaps)
$(BUILD)/disk-p17.img: $(BUILD)/BOOTX64.EFI \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm services/init/init.wasm \
                       services/shell/shell.wasm services/net/net.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nnet net.wasm 0x1022\nlogin login.wasm 0x1008\nshell shell.wasm 0x100C\n' > $(BUILD)/init-p17.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm services/net/net.wasm:/boot/modules/net.wasm \
	  $(BUILD)/init-p17.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

 .PHONY: test-p17
 test-p17: VARS := $(BUILD)/vars/p17.fd
 test-p17: $(BUILD)/disk-p17.img $(BUILD)/vars/p17.fd
	bash scripts/run_p17.sh $(BUILD)/serial-p17.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p17.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial-p17.log \
		&& grep -qaE '\[sh[^]]*\] well-known ports:' $(BUILD)/serial-p17.log \
		&& grep -qaE '\[sh[^]]*\] (init|console|fs|net|login|shell) +sid=[1-9][0-9]* +uid=0' $(BUILD)/serial-p17.log \
		&& grep -qaE '\[sh[^]]*\] CAP_SPAWN' $(BUILD)/serial-p17.log \
		&& grep -qaE '\[sh[^]]*\] usage: chcaps' $(BUILD)/serial-p17.log \
		&& grep -qa 'chcaps: ok' $(BUILD)/serial-p17.log \
		&& grep -qa 'op=CHCAPS' $(BUILD)/serial-p17.log \
		&& echo "TEST PASS (p17 capability & port introspection)" \
		|| { echo "TEST FAIL (p17)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p17.log | tail -40; exit 1; }

 # Phase 18 disk: package management (pkg list/install/remove with sig verify)
# Phase 18 signing chain: host-side ed25519 signer (tools/module-sign), the
# trusted-key material, and the signed + deliberately-mismatched modules the
# `module verify` gate asserts on. The gate's real claim is the REJECTION
# path, hence bad.wasm (wrong abi_ver + bad signature) alongside the good one.
MODULE_SIGN := $(BUILD)/module-sign
$(MODULE_SIGN): tools/module-sign/go.mod tools/module-sign/main.go | $(BUILD)
	cd tools/module-sign && go build -o ../../$@ .

$(BUILD)/trusted-keys.json: tools/module-sign/main.go | $(MODULE_SIGN) $(BUILD)
	$(MODULE_SIGN) genkey > $@

$(BUILD)/etc_trusted: $(BUILD)/trusted-keys.json
	@python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["public"])' $< > $@

$(BUILD)/fs.signed.wasm: services/fs/fs.wasm $(MODULE_SIGN) $(BUILD)/trusted-keys.json
	$(MODULE_SIGN) sign $$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["private"])' $(BUILD)/trusted-keys.json) $< $@ 2

$(BUILD)/hello.signed.wasm: services/hello/hello.wasm $(MODULE_SIGN) $(BUILD)/trusted-keys.json
	$(MODULE_SIGN) sign $$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["private"])' $(BUILD)/trusted-keys.json) $< $@ 2

# Minimal test wasm for pkg install (195 bytes vs hello.wasm's 2MB) to avoid
# multi-second port round-trips through the 4096-byte port message cap.
$(BUILD)/hello_min.wasm: guests/hello.wat | $(BUILD)
	$(WAT2WASM) $< -o $@

$(BUILD)/hello_min_abi.wasm: $(BUILD)/hello_min.wasm | $(BUILD)
	python3 -c 'import sys; d=bytearray(open(sys.argv[1],"rb").read()); n=b"abi_ver"; p=b"\x02"; b=bytes([len(n)])+n+p; d.extend(bytes([0,len(b)])+b); open(sys.argv[2],"wb").write(d)' $< $@

$(BUILD)/hello_test_signed.wasm: $(BUILD)/hello_min_abi.wasm $(MODULE_SIGN) $(BUILD)/trusted-keys.json
	cp $< $(BUILD)/hello
	$(MODULE_SIGN) sign $$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["private"])' $(BUILD)/trusted-keys.json) $(BUILD)/hello $@ 2
	rm -f $(BUILD)/hello

# mismatch: a module that must FAIL verification (wrong abi_ver + bad sig)
# Uses the minimal wasm to avoid multi-second serial round-trips.
$(BUILD)/bad.wasm: $(BUILD)/hello_min_abi.wasm $(MODULE_SIGN) | $(BUILD)
	$(MODULE_SIGN) -mismatch $< $@

# tamper: a module with correct abi_ver=2 but a single flipped signature byte.
# This isolates the signature-rejection path from the abi_ver-mismatch path
# that -mismatch covers (which also uses a new key). Together the two modules
# prove both rejection axes the Phase 18 gate asserts.
$(BUILD)/tampered.wasm: $(BUILD)/hello_test_signed.wasm $(MODULE_SIGN) | $(BUILD)
	$(MODULE_SIGN) -tamper $< $@

# Everything pkg/module verify touches must live in the KFS1 volume, because
# that is the volume fs.wasm actually serves. /etc/trusted in particular is
# read through fs.wasm, so a copy left only in the FAT16 boot volume reads as
# "no trusted keys loaded" and every signature check rejects.
$(BUILD)/p18-seed.stamp: $(BUILD)/fs.signed.wasm $(BUILD)/bad.wasm \
                        $(BUILD)/tampered.wasm $(BUILD)/hello_test_signed.wasm $(BUILD)/etc_trusted
	rm -rf $(BUILD)/p18-seed
	mkdir -p $(BUILD)/p18-seed/boot/modules $(BUILD)/p18-seed/tmp $(BUILD)/p18-seed/etc
	cp $(BUILD)/fs.signed.wasm $(BUILD)/p18-seed/boot/modules/fs.wasm
	cp $(BUILD)/bad.wasm $(BUILD)/p18-seed/tmp/bad.wasm
	cp $(BUILD)/tampered.wasm $(BUILD)/p18-seed/tmp/tampered.wasm
	cp $(BUILD)/hello_test_signed.wasm $(BUILD)/p18-seed/tmp/hello.wasm
	cp $(BUILD)/etc_trusted $(BUILD)/p18-seed/etc/trusted
	touch $@

# Phase 18's data volume rides in the SAME image as the boot volume.
#
# It used to be a second virtio-blk disk, which cannot work: the kernel's
# virtio-blk driver is single-device by construction (vmod_probe returns the
# first match and stops) and the block import pair kern_blk_read/write
# carries no device selector, so the guest never saw the seed volume at all
# and fs.wasm correctly reported "no KFS1 superblock". Supporting a second
# disk is an ABI change, which is not something to smuggle into a gate fix.
#
# One disk with two volumes needs no ABI change and no kernel change: FAT16
# at sector 0 for the loader's ESP, KFS1 after the boundary kfs.SplitSectors
# derives. fs.wasm locates it with kfs.FindVolume under the same rule, so the
# builder and the guest cannot disagree about where the volume starts.
$(BUILD)/disk-p18.img: $(BUILD)/BOOTX64.EFI \
                        $(BUILD)/fs.signed.wasm services/console/console.wasm \
                        services/login/login.wasm services/init/init.wasm \
                        services/shell/shell.wasm services/net/net.wasm \
                        $(BUILD)/etc_users.txt $(BUILD)/etc_trusted \
                        $(BUILD)/bad.wasm $(BUILD)/tampered.wasm $(BUILD)/hello_test_signed.wasm \
                        $(BUILD)/p18-seed.stamp | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nnet net.wasm 0x1022\nlogin login.wasm 0x1008\nshell shell.wasm 0x1058\n' > $(BUILD)/init-p18.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  -kfs-seed $(BUILD)/p18-seed -kfs-prefix / \
	  $(BUILD)/fs.signed.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm services/net/net.wasm:/boot/modules/net.wasm \
	  $(BUILD)/init-p18.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users \
	  $(BUILD)/etc_trusted:/etc/trusted \
	  $(BUILD)/bad.wasm:/tmp/bad.wasm \
	  $(BUILD)/tampered.wasm:/tmp/tampered.wasm \
	  $(BUILD)/hello_test_signed.wasm:/tmp/hello.wasm

  # The guest reaches the image over virtio-blk, which is the only block
# transport the kernel block imports (kern_blk_read/write) are wired to.
# QEMU_BASE attaches nothing to virtio; the firmware reads the same image as
# SATA, so one file serves both roles.
# snapshot=on gives the guest a private copy-on-write view: QEMU refuses a
# read-write lock on a file the firmware already has open, and the gate has no
# reason to persist guest writes anyway.
P18_VIRTIO := -drive id=p18,format=raw,file=$(BUILD)/disk-p18.img,if=none,snapshot=on -device virtio-blk-pci,drive=p18

  .PHONY: test-p18
  test-p18: VARS := $(BUILD)/vars/p18.fd
# p18 instantiates fs.wasm, shell.wasm and pkg's ed25519 path at once, and
# module verify holds a whole module in memory while hashing it. At the 512 MB
# default the shell's Go heap ran out mid-verify ("fatal error: out of memory",
# pkg.go Verify) and the gate failed for a reason that had nothing to do with
# package management. 1 GiB is what this gate has always needed; the earlier
# rewrite of this rule dropped the setting and only got away with it because
# its fs.wasm happened to be 123 KB smaller.
  test-p18: QEMU_MEM := 1024
  test-p18: $(BUILD)/disk-p18.img $(BUILD)/vars/p18.fd
	bash scripts/run_p18.sh $(BUILD)/serial-p18.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p18.img,snapshot=on $(P18_VIRTIO) $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial-p18.log \
		&& grep -qaE '\] (fs|console|login|init|shell|net)' $(BUILD)/serial-p18.log \
		&& grep -qa 'module verify: /boot/modules/fs.wasm: ok' $(BUILD)/serial-p18.log \
		&& grep -qa 'module verify: /tmp/bad.wasm: rejected' $(BUILD)/serial-p18.log \
		&& grep -qa 'module verify: /tmp/tampered.wasm: rejected' $(BUILD)/serial-p18.log \
		&& grep -qa 'installed hello' $(BUILD)/serial-p18.log \
		&& grep -qa 'removed hello' $(BUILD)/serial-p18.log \
		&& grep -qa 'spawned sid=' $(BUILD)/serial-p18.log \
		&& echo "TEST PASS (p18 package management + module verify)" \
		|| { echo "TEST FAIL (p18)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p18.log | tail -40; exit 1; }

 # Phase 19 disk: supervision & config control surface. The boot shell holds
 # CAP_CONF here (0x1088 = FOCUS|CONF|PORTBIND) so the serial gate can prove
 # the full sysctl-write -> knob-store -> scheduler-quantum path live; no
 # other disk grants shells CONF.
$(BUILD)/disk-p19.img: $(BUILD)/BOOTX64.EFI \
                       services/fs/fs.wasm services/console/console.wasm \
                       services/login/login.wasm services/init/init.wasm \
                       services/shell/shell.wasm \
                       $(BUILD)/etc_users.txt | $(BUILD) $(IMG)
	printf 'console console.wasm 0x1000\nfs fs.wasm 0x1018\nlogin login.wasm 0x1008\nshell shell.wasm 0x1088\n' > $(BUILD)/init-p19.conf.tmp
	$(IMG) -o $@ -size 64M -efi $(BUILD)/BOOTX64.EFI \
	  services/fs/fs.wasm:/boot/modules/fs.wasm services/console/console.wasm:/boot/modules/console.wasm services/login/login.wasm:/boot/modules/login.wasm \
	    services/init/init.wasm:/boot/modules/init.wasm services/shell/shell.wasm:/boot/modules/shell.wasm \
	  $(BUILD)/init-p19.conf.tmp:/init.conf \
	  $(BUILD)/etc_users.txt:/etc/users

 .PHONY: test-p19
 test-p19: VARS := $(BUILD)/vars/p19.fd
 test-p19: $(BUILD)/disk-p19.img $(BUILD)/vars/p19.fd
	bash scripts/run_p19.sh $(BUILD)/serial-p19.log "$(QEMU)" "$(QEMU_ENV)" -- \
	    -drive format=raw,file=$(BUILD)/disk-p19.img $(QEMU_BASE)
	@grep -qa 'shell ready' $(BUILD)/serial-p19.log \
		&& grep -qa 'quantum_us = 5000' $(BUILD)/serial-p19.log \
		&& grep -qa 'quantum_us = 20000' $(BUILD)/serial-p19.log \
		&& grep -qa 'quantum=20000us' $(BUILD)/serial-p19.log \
		&& grep -qa 'source=init' $(BUILD)/serial-p19.log \
		&& grep -qa 'checkconf: OK' $(BUILD)/serial-p19.log \
		&& grep -qa 'initctl: restart fs ok' $(BUILD)/serial-p19.log \
		&& echo "TEST PASS (p19 supervision & config)" \
		|| { echo "TEST FAIL (p19)"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial-p19.log | tail -40; exit 1; }

test-all: check-makefile-intact check-gate-manifest check-image-config check-cmd-coverage check-artifact-freshness test-vet test-race test-kernel test-ctx-fpu test-fmt test-oracle test-wasm-interp test-wasm-artifacts test-wasm-fuzz test-wasm-owned test-wasm-wasi test-wasm-wasi-artifacts test-unit test-g1 test-g1-owned test-g2 test-g2-owned test-g3 test-g3-owned test-p4 test-p4-owned test-p5a test-p5b test-p7 test-p7-owned test-p8a test-p8-owned test-p8b test-p9 test-p10 test-p11 test-p11-owned test-p11b test-p11b-owned test-p12 test-p12-owned test-p13 test-p13-owned test-p14 test-p14sh test-p15 test-p16 test-p16-owned test-p17 test-p18 test-p19 test-p11-gfx

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

