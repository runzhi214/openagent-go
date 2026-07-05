// Echo tool plugin — pure WASM, #![no_std], zero imports.
//
// Build:
//   rustc --target wasm32-unknown-unknown -C opt-level=z --edition 2021 \
//         --crate-type cdylib -C link-arg=--no-entry -o echo.wasm echo.rs
//
// ABI exports:
//   alloc(size: i32) -> i32
//   metadata() -> i64
//   execute(ptr: i32, len: i32) -> i64

#![no_std]
#![no_main]

use core::panic::PanicInfo;

#[panic_handler]
fn panic(_: &PanicInfo) -> ! {
    loop {}
}

// ── Bump allocator ──

const HEAP_SIZE: usize = 8192;
static mut HEAP: [u8; HEAP_SIZE] = [0; HEAP_SIZE];
static mut HEAP_OFF: usize = 0;

#[no_mangle]
pub extern "C" fn alloc(size: u32) -> u32 {
    let size = size as usize;
    let aligned = (size + 7) & !7;
    unsafe {
        let ptr = HEAP.as_ptr() as u32 + HEAP_OFF as u32;
        if HEAP_OFF + aligned <= HEAP_SIZE {
            HEAP_OFF += aligned;
        }
        ptr
    }
}

// ── pack helper: (ptr: u32, len: u32) → u64 ──

fn pack(ptr: u32, len: u32) -> u64 {
    ((ptr as u64) << 32) | (len as u64)
}

// ── Metadata ──

const META: &str = r#"{"type":"tool","name":"echo","description":"Echoes back the input message","parameters":{"type":"object","properties":{"message":{"type":"string","description":"The message to echo"}},"required":["message"]}}"#;

#[no_mangle]
pub extern "C" fn metadata() -> u64 {
    pack(META.as_ptr() as u32, META.len() as u32)
}

// ── Execute (tool plugin entry point) ──

#[no_mangle]
pub extern "C" fn execute(ptr: u32, len: u32) -> u64 {
    let input = unsafe { core::slice::from_raw_parts(ptr as *const u8, len as usize) };

    // Find "message":" in JSON input
    let pattern = b"\"message\":\"";
    let mut msg_start = 0usize;
    for i in 0..input.len().saturating_sub(pattern.len()) {
        if &input[i..i + pattern.len()] == pattern {
            msg_start = i + pattern.len();
            break;
        }
    }

    // Find closing quote
    let mut msg_end = msg_start;
    while msg_end < input.len() && input[msg_end] != b'"' {
        msg_end += 1;
    }
    let msg = &input[msg_start..msg_end];

    // Build output: {"result":"you said: <msg>"}
    let pre = b"{\"result\":\"you said: ";
    let post = b"\"}";
    let out_len = pre.len() + msg.len() + post.len();
    let out_ptr = alloc(out_len as u32);
    let out = unsafe { core::slice::from_raw_parts_mut(out_ptr as *mut u8, out_len) };

    out[..pre.len()].copy_from_slice(pre);
    out[pre.len()..pre.len() + msg.len()].copy_from_slice(msg);
    out[pre.len() + msg.len()..].copy_from_slice(post);

    pack(out_ptr, out_len as u32)
}
