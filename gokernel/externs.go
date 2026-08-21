package main

import "unsafe"

// Assembly-implemented primitives (ports_amd64.s, vec_amd64.s).

func putc(c byte)
func halt()
func cpuidAvx2() bool
func enableAVX2()

func vecLoad(src, dst unsafe.Pointer)
func vecStore(src, dst unsafe.Pointer)
func vecBcast(val, cls uint64, dst unsafe.Pointer)
func vecSub(a, b unsafe.Pointer, cls uint64, dst unsafe.Pointer)
func vecCmpEqAll(a, b unsafe.Pointer, cls uint64, dst unsafe.Pointer) bool
