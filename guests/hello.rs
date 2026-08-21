//! guest #2: stock rustc --target wasm32-wasip1, no_std, no wasi-libc deps
//! beyond what its crt pulls in (kernel stubs cover linkage extras).
#![no_std]
#![no_main]

#[repr(C)]
struct IoVec {
    ptr: *const u8,
    len: u32,
}

#[link(wasm_import_module = "wasi_snapshot_preview1")]
extern "C" {
    fn fd_write(fd: i32, iovs: *const IoVec, iovs_len: i32, nwritten: *mut u32) -> i32;
    fn proc_exit(code: i32);
}

static HELLO: &[u8] = b"hello from Rust\n";

#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    loop {}
}

#[no_mangle]
pub extern "C" fn _start() {
    unsafe {
        let iov = IoVec {
            ptr: HELLO.as_ptr(),
            len: HELLO.len() as u32,
        };
        let mut n: u32 = 0;
        let _ = fd_write(1, &iov, 1, &mut n);
        proc_exit(0);
    }
}
