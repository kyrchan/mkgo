# go-kernel: UEFI microkernel hosting the Go runtime + restricted-ISA VM
#
#   C shim (firmware handshake)  ->  build/BOOTX64.EFI  (merged PE32+)
#   Go kernel (gokernel pkg)     ->  build/kernel.elf / .bin
#   guest programs               ->  programs/*.vbin via tools/vasm

BUILD := build
ROOT  ?= $(HOME)/.local/osdev-root

QEMU    := $(shell command -v qemu-system-x86_64 2>/dev/null || echo $(ROOT)/usr/bin/qemu-system-x86_64)
MFORMAT := $(shell command -v mformat 2>/dev/null || echo $(ROOT)/usr/bin/mformat)
MMD     := $(shell command -v mmd 2>/dev/null || echo $(ROOT)/usr/bin/mmd)
MCOPY   := $(shell command -v mcopy 2>/dev/null || echo $(ROOT)/usr/bin/mcopy)
OBJCOPY := objcopy

GOROOT_BARE ?= $(HOME)/local/goroot-bare
GOROOT_BARE := $(HOME)/.local/goroot-bare
GOBARE      := GOROOT=$(GOROOT_BARE) PATH=$(GOROOT_BARE)/bin:$(PATH) GO111MODULE=off GOOS=baremetal GOARCH=amd64

OVMF_CODE := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_CODE_4M.fd) $(wildcard $(d)/OVMF_CODE.fd)))
OVMF_VARS := $(firstword $(foreach d,$(ROOT)/usr/share/OVMF /usr/share/OVMF /usr/share/ovmf,$(wildcard $(d)/OVMF_VARS_4M.fd) $(wildcard $(d)/OVMF_VARS.fd)))

QEMU_ENV := LD_LIBRARY_PATH=$(ROOT)/usr/lib/x86_64-linux-gnu
KVM_FLAG := $(shell [ -w /dev/kvm ] && echo -enable-kvm || echo -accel tcg)

CC      := gcc
CFLAGS  := -std=c11 -ffreestanding -fno-stack-protector -fno-stack-clash-protection \
           -mno-red-zone -fno-pic -mcmodel=small -Os -g -Wall -Wextra -Ikernel
LDFLAGS := -nostdlib -no-pie -Wl,--build-id=none -Wl,-e,efi_main -T kernel/link.ld

GO_BASE := 0x400000

SHIM_OBJS := $(BUILD)/main.o $(BUILD)/serial.o $(BUILD)/cpu.o $(BUILD)/mm.o \
             $(BUILD)/gdt_idt.o $(BUILD)/lib.o $(BUILD)/loader.o

.PHONY: all run test clean image tools

all: image

$(BUILD):
	mkdir -p $@

$(BUILD)/%.o: kernel/%.c $(wildcard kernel/*.h) | $(BUILD)
	$(CC) $(CFLAGS) -c $< -o $@

$(BUILD)/kernel.so: $(SHIM_OBJS) $(BUILD)/goshim.a kernel/link.ld | $(BUILD)
	$(CC) $(LDFLAGS) $(SHIM_OBJS) $(BUILD)/goshim.a -o $@

# Plan 9 asm IDT stub bank, assembled by go tool asm via c-archive
$(BUILD)/goshim.a: tools/goshim/shim.go tools/goshim/gen_vectors.s | $(BUILD)
	cd tools/goshim && CGO_ENABLED=1 go build -buildmode=c-archive -o ../../$@ .
	objcopy --globalize-symbol=isr_stub_table --globalize-symbol=isr_dump_ptr \
	    $@ $@.tmp && mv $@.tmp $@

tools:
	cd tools/vasm && go build -o ../../$(BUILD)/vasm .

programs/demo.vbin: programs/demo.vasm tools
	$(BUILD)/vasm $< $@

# ---- Go kernel ----
$(BUILD)/kernel.elf: $(wildcard gokernel/*.go gokernel/*.s) | $(BUILD)
	$(GOBARE) go build -o $(BUILD)/kernel.elf ./gokernel
	

$(BUILD)/goaddr.mk: $(BUILD)/kernel.elf scripts/goaddr.sh
	bash scripts/goaddr.sh $< $@

-include $(BUILD)/goaddr.mk

$(BUILD)/BOOTX64.EFI: $(BUILD)/kernel.so $(BUILD)/kernel.elf $(BUILD)/goaddr.mk scripts/mkpefi.py
	python3 scripts/mkpefi.py $(BUILD)/kernel.so $(BUILD)/kernel.elf \
	    $(GO_BASE) $(GO_ENTRY) $(GO_END) $@

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
	@grep -q 'KERNEL-OK' $(BUILD)/serial.log && grep -q 'out 0x28' $(BUILD)/serial.log \
		&& echo "TEST PASS" || { echo "TEST FAIL"; sed -e 's/\x1b\[[0-9;]*[A-Za-z]//g' $(BUILD)/serial.log | tail -30; exit 1; }

clean:
	rm -rf $(BUILD)
