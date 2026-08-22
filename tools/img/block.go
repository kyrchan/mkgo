package main

import (
	"fmt"
	"io"
	"os"
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

// FlushToFile writes the whole block content to path (creating/truncating).
func FlushToFile(b BlockDevice, path string) error {
	mb, ok := b.(*MemBlock)
	if !ok {
		return fmt.Errorf("flush: unsupported device type %T", b)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(mb.data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
