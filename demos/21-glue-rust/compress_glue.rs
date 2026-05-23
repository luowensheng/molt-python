// compress_glue.rs — thin Rust wrapper, callable from Python via transport glue.
//
// This file is NOT a Rust crate — it's included directly into molt's generated
// server via `include!("compress_glue.rs")`. All functions must be `pub`.
//
// The `flate2` crate is declared in moltproject.toml under `crates = [...]`.
// molt adds it to the generated Cargo.toml automatically.

use flate2::Compression;
use flate2::read::ZlibDecoder;
use flate2::write::ZlibEncoder;
use std::io::Read;  // Write is already imported by the generated server wrapper

/// Compress data using zlib deflate.
pub fn deflate(data: Vec<u8>) -> Vec<u8> {
    let mut enc = ZlibEncoder::new(Vec::new(), Compression::default());
    enc.write_all(&data).unwrap_or(());
    enc.finish().unwrap_or_default()
}

/// Decompress zlib-compressed data.
pub fn inflate(data: Vec<u8>) -> Vec<u8> {
    let mut dec = ZlibDecoder::new(data.as_slice());
    let mut out = Vec::new();
    dec.read_to_end(&mut out).unwrap_or(0);
    out
}

/// Return the compression ratio (compressed_len / original_len).
/// Values below 1.0 mean the data compressed well.
pub fn ratio(data: Vec<u8>) -> f64 {
    if data.is_empty() {
        return 1.0;
    }
    let compressed = deflate(data.clone());
    compressed.len() as f64 / data.len() as f64
}
