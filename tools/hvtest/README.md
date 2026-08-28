# hvtest — headless hypervisor test matrix

Boots the **same** raw `disk.img` under every available hypervisor and
asserts the **identical gate strings** on serial output, so a firmware or
device-model difference between QEMU, VirtualBox, and VMware surfaces as a
gate failure instead of a silent divergence (Phase-12 preparation).

```
backend  status detail
qemu     PASS  all gates hit
vbox     SKIP  VBoxManage not installed (skip: install VirtualBox ...)
vmware   SKIP  vmrun not installed (skip: install VMware ...)
```

## Usage

```sh
cd tools/hvtest && go build -o hvtest .

hvtest -img build/disk.img -gates 'KERNEL-OK' all       # full matrix
hvtest -img build/disk.img qemu                         # one backend
hvtest -img ... -gates 'KERNEL-OK,out 0x28' -timeout 90s
```

Exit codes: `0` when every backend passed or skipped; `1` if any failed;
`2` on usage/input errors. Gates are plain substrings of ANSI-stripped,
CRLF-normalized serial output — the same convention as the Makefile's
`make test` greps.

## Backends

| backend | tool | serial transport | notes |
|---|---|---|---|
| `qemu`   | `qemu-system-x86_64` | `-serial file:` | reference path; mirrors the Makefile's q35 + OVMF pflash setup; finds binaries/firmware in PATH or `~/.local/osdev-root` |
| `vbox`   | `VBoxManage`         | uart1 in *server* mode → unix socket, streamed by this harness | image converted with `convertfromraw` per run; VM torn down after |
| `vmware` | `vmrun`              | `serial0.fileType="file"` polled like QEMU | `.vmx` generated for EFI boot from a monolithicFlat VMDK written by this tool |

## Prerequisites (stated honestly)

- **QEMU**: required to be present for the reference run. Looked up in
  `PATH`, then `~/.local/osdev-root/usr/bin` (with its private
  `LD_LIBRARY_PATH` and `-L` firmware dirs applied automatically).
  OVMF code/vars pair looked up under `~/.local/osdev-root/usr/share/OVMF`,
  `/usr/share/OVMF`, `/usr/share/ovmf`.
- **VirtualBox**: usually NOT installed in dev containers here. If absent,
  the vbox row reports SKIP with install guidance (`VBoxManage` on PATH).
- **VMware**: likewise SKIP unless `vmrun` is on PATH.

SKIP never masks failure: a green `all` run means "everything installed
passed", and the matrix shows exactly what was exercised.

## Testing the harness itself

The gate matcher, .vmx generator, VMDK writer, and every command-line
builder are unit-tested without any hypervisor installed:

```sh
go test .
```

A live smoke of the QEMU backend against any bootable image:

```sh
hvtest -img <bootable.img> -gates 'BdsDxe' -timeout 15s qemu
```
