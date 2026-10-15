(module
  (type $write_type (func (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (type $write_type)))
  (memory (export "memory") 1)
  (data (i32.const 0) "busy-start\n")
  (func (export "_start")
    i32.const 64
    i32.const 0
    i32.store
    i32.const 68
    i32.const 11
    i32.store
    i32.const 1
    i32.const 64
    i32.const 1
    i32.const 100
    call $fd_write
    drop
    (loop $forever
      br $forever)))
