;; guest #1: hand-written WASI hello (wat2wasm). Imports exactly the
;; frozen profile subset -- the smallest possible engine validation.
(module
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "proc_exit"
    (func $proc_exit (param i32)))

  (memory (export "memory") 1)

  (data (i32.const 8) "hello from C\n")

  (func $start (export "_start")
    ;; iov[0] = {ptr=8, len=13} at address 0
    (i32.store (i32.const 0) (i32.const 8))
    (i32.store (i32.const 4) (i32.const 13))
    (drop (call $fd_write
      (i32.const 1)   ;; stdout
      (i32.const 0)   ;; iovs
      (i32.const 1)   ;; iovs_len
      (i32.const 24))) ;; nwritten scratch
    (call $proc_exit (i32.const 0)))
)
