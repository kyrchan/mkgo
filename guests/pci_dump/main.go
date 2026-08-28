//go:build wasip1

package main

import (
	"fmt"
	"os"

	lib "kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()
	dm, err := lib.BindDevman(k)
	if err != nil {
		fmt.Println("bind devman failed:", err)
		os.Exit(0)
	}
	recs, err := dm.Enum()
	if err != nil {
		fmt.Println("enum failed:", err)
		os.Exit(0)
	}
	fmt.Printf("devman: %d devices\n", len(recs))
	for _, r := range recs {
		fmt.Printf("  class=%d inst=0x%x win=0x%x\n", r.Class, r.Inst, r.WinOff)
	}
	os.Exit(0)
}
