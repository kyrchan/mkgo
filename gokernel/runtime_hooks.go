package main

import "runtime"

// Hook into the runtime's baremetal port: the asm entry (_rt0_amd64_baremetal)
// stashes the firmware shim's boot-info pointer; the runtime exposes it here.
func bootInfoRaw() uintptr { return runtime.BaremetalBootInfoRaw() }
