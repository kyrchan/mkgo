package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BlockDevice is the minimal block-storage surface the image builder needs.
// The FAT16 volume builder writes exclusively through this interface, which
// lets every code path be tested against an in-memory device (MemBlock).
type BlockDevice interface {
	Size() int64
	io.ReaderAt
	io.WriterAt
}

// MemBlock is an in-memory BlockDevice of a fixed size.
type MemBlock struct {
	data []byte
}

func NewMemBlock(size int64) *MemBlock {
	return &MemBlock{data: make([]byte, size)}
}

func (b *MemBlock) Size() int64 { return int64(len(b.data)) }

// Bytes exposes the backing store (tests and final image flush).
func (b *MemBlock) Bytes() []byte { return b.data }

func (b *MemBlock) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b.data)) {
		return 0, fmt.Errorf("memblock: read offset %d out of range", off)
	}
	n := copy(p, b.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (b *MemBlock) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b.data)) {
		return 0, fmt.Errorf("memblock: write offset %d out of range", off)
	}
	if int64(len(p)) > int64(len(b.data))-off {
		return 0, fmt.Errorf("memblock: write at %d overflows device", off)
	}
	copy(b.data[off:], p)
	return len(p), nil
}

// FlushToFile atomically publishes the whole block content at path.
func FlushToFile(b BlockDevice, path string) error {
	mb, ok := b.(*MemBlock)
	if !ok {
		return fmt.Errorf("flush: unsupported device type %T", b)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		tmp.Close()
		if !keep {
			os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, bytes.NewReader(mb.data)); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}
