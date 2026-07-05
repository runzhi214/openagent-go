// Stage logger plugin — pure WASM, #![no_std], zero imports.
//
// Receives stage events from the runner, extracts the event name and phase
// from the JSON input, and returns a "continue" action that includes the
// extracted info — proving the WASM plugin's own logic processed the input.
//
// Build:
//   rustc --target wasm32-unknown-unknown -C opt-level=z --edition 2021 \
//         --crate-type cdylib -C link-arg=--no-entry -o logger.wasm logger.rs

#![no_std]
#![no_main]

use core::panic::PanicInfo;

#[panic_handler]
fn panic(_: &PanicInfo) -> ! {
    loop {}
}

// ── Bump allocator ──

const HEAP_SIZE: usize = 4096;
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

fn pack(ptr: u32, len: u32) -> u64 {
    ((ptr as u64) << 32) | (len as u64)
}

// ── Helpers ──

fn find_str(haystack: &[u8], needle: &str) -> usize {
    let n = needle.as_bytes();
    let end = haystack.len().saturating_sub(n.len());
    for i in 0..end {
        if &haystack[i..i + n.len()] == n {
            return i + n.len();
        }
    }
    0
}

fn extract_quoted(input: &[u8], start: usize) -> &[u8] {
    let mut end = start;
    while end < input.len() && input[end] != b'"' {
        end += 1;
    }
    &input[start..end]
}

// ── Metadata ──

const META: &str = r#"{"type":"stage","name":"stage_logger","stage":"*","phase":"*"}"#;

#[no_mangle]
pub extern "C" fn metadata() -> u64 {
    pack(META.as_ptr() as u32, META.len() as u32)
}

// ── Run (stage plugin entry point) ──

#[no_mangle]
pub extern "C" fn run(ptr: u32, len: u32) -> u64 {
    let input = unsafe { core::slice::from_raw_parts(ptr as *const u8, len as usize) };

    // Extract "name" and "phase" from JSON input to prove we processed it.
    let name_start = find_str(input, "\"name\":\"");
    let name = extract_quoted(input, name_start);

    let phase_start = find_str(input, "\"phase\":\"");
    let phase = extract_quoted(input, phase_start);

    // Build response: {"action":"continue","from":"wasm_stage_logger","event":"...","phase":"..."}
    let prefix = b"{\"action\":\"continue\",\"from\":\"wasm_stage_logger\",\"event\":\"";
    let mid = b"\",\"phase\":\"";
    let suffix = b"\"}";
    let out_len = prefix.len() + name.len() + mid.len() + phase.len() + suffix.len();
    let out_ptr = alloc(out_len as u32);
    let out = unsafe { core::slice::from_raw_parts_mut(out_ptr as *mut u8, out_len) };

    let mut pos = 0;
    out[pos..pos + prefix.len()].copy_from_slice(prefix); pos += prefix.len();
    out[pos..pos + name.len()].copy_from_slice(name);     pos += name.len();
    out[pos..pos + mid.len()].copy_from_slice(mid);       pos += mid.len();
    out[pos..pos + phase.len()].copy_from_slice(phase);   pos += phase.len();
    out[pos..pos + suffix.len()].copy_from_slice(suffix);

    pack(out_ptr, out_len as u32)
}
