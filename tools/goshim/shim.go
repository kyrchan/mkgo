package main

// stubTablePtr is implemented in gen_vectors.s.
func stubTablePtr() *[256]uintptr

// Referencing the stub bank from a package-level initializer makes the Go
// linker retain it (and every isr_* symbol it points at) in the archive.
var _keep = stubTablePtr()

// main is never called: only the assembly symbols from gen_vectors.s are
// pulled out of the archive by the kernel linker.
func main() {}
