// Package jitc is the Go JIT/AOT compiler server for the capability
// microkernel (HP-JIT work order, Stage 1+). It decodes stock-toolchain
// `.wasm` modules (MVP subset) into IR and — in later stages — emits
// x86_64 machine code plus stack maps / safe-point tables for the
// kernel's native-execution cache (`services/ABI-NOTES.md` §12).
//
// Stage 1 scope: LEB128 + module/section decoder + function-body IR.
// Every read is bounds-checked; malformed input returns an error, never
// panics (fuzzed per engineering practice #4). No wasm3/WAMR dependency:
// this package only reads bytes.
package jitc
