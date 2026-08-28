#!/bin/bash
# demo-graphics.sh — boot the microkernel with graphics.wasm rendering /etc/motd
# to a visible QEMU window via VFIO-style display controller (bochs-display).
set -e
cd "$(dirname "$0")"

echo "=== Building kernel + service modules ==="
make build/BOOTX64.EFI build/disk-p11b.img 2>&1 | tail -3

echo "=== Launching QEMU with display window ==="
echo "The window shows graphics.wasm rendering /etc/motd to the framebuffer."
echo "Close the window (or Ctrl+C) to quit."
echo ""

# Kill any stray QEMU to avoid window clutter
pkill -f "disk-p11b.img" 2>/dev/null || true
sleep 0.5

# Note: NO isa-debug-exit here — we want the window to stay up after the
# guest renders. The rendered pixels persist in the bochs-display framebuffer
# and QEMU scans them out continuously.
env LD_LIBRARY_PATH=/home/cyr/.local/osdev-root/usr/lib/x86_64-linux-gnu \
  /usr/bin/qemu-system-x86_64 \
  -L /home/cyr/.local/osdev-root/usr/share/qemu \
  -L /home/cyr/.local/osdev-root/usr/share/seabios \
  -machine q35 -cpu max -m 512 -accel tcg \
  -drive if=pflash,format=raw,readonly=on,file=/home/cyr/.local/osdev-root/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,file=build/VARS.fd \
  -device bochs-display \
  -display sdl \
  -net none \
  -drive format=raw,file=build/disk-p11b.img \
  -serial file:build/serial-demo.log 2>&1 | tail -5

echo "=== Serial log (proof of render) ==="
grep -E "scanout|fb_present|all ok" build/serial-demo.log 2>/dev/null | sed 's/\x1b\[[0-9;]*[A-Za-z]//g' | head
