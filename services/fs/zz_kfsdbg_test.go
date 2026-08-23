package main

import (
	"testing"
)

func TestDebugPacing(t *testing.T) {
	w := buildWorkload(t)
	k := w.k
	if err := k.Mkdir("/tmp"); err != nil && err != ErrExists {
		t.Fatal(err)
	}
	k.Create("/tmp/log")
	prev := k.wOff
	for i := 0; i < 80; i++ {
		err := k.WriteFile("/tmp/log", uint64(i)*4, []byte("abcd"))
		if err != nil {
			t.Fatalf("i=%d: %v", i, err)
		}
		if i < 6 || k.wOff-prev > 200 {
			t.Logf("i=%d wOff=%d delta=%d dataLen=%d", i, k.wOff, k.wOff-prev, len(k.data[k.inoOfLog()]))
		}
		prev = k.wOff
	}
}

func (k *KFS) inoOfLog() uint32 { return uint32(9) }
