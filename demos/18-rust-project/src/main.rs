// main.rs — statistics CLI in Rust (std library only).
// Demonstrates: iterators, pattern matching, trait objects, formatted output.
//
// Build: molt run build   (cargo build --release → target/release/stats)
// Run:   molt run stats <number> [<number>...]
// Demo:  molt run demo
//
// Adding external crates:
//   molt add serde --features derive   →  cargo add serde --features derive
//   molt sync                          →  cargo fetch

use std::env;
use std::process;

// ── Stats result ────────────────────────────────────────────────────────────

/// Descriptive statistics computed from a non-empty slice of f64.
struct Stats {
    n:       usize,
    sum:     f64,
    mean:    f64,
    std_dev: f64,
    min:     f64,
    max:     f64,
}

/// Compute descriptive statistics over a non-empty slice.
/// Returns None if the slice is empty.
fn compute(data: &[f64]) -> Option<Stats> {
    if data.is_empty() {
        return None;
    }

    let n = data.len();

    // min / max via fold — avoids a second pass with a separate iterator
    let (min, max) = data.iter().fold((data[0], data[0]), |(lo, hi), &x| {
        (lo.min(x), hi.max(x))
    });

    let sum: f64  = data.iter().sum();
    let mean      = sum / n as f64;

    // Two-pass variance for numerical stability
    let variance: f64 = data.iter()
        .map(|&x| { let d = x - mean; d * d })
        .sum::<f64>()
        / n as f64;
    let std_dev = variance.sqrt();

    Some(Stats { n, sum, mean, std_dev, min, max })
}

// ── Histogram ───────────────────────────────────────────────────────────────

/// Print a text histogram of `data` using `buckets` equally-spaced bins.
/// Bar width is normalised to `bar_width` characters.
fn print_histogram(data: &[f64], min: f64, max: f64, buckets: usize, bar_width: usize) {
    let range = max - min;
    if range <= 0.0 {
        return;
    }

    // Count values into each bucket
    let mut counts = vec![0usize; buckets];
    for &x in data {
        // Map x into [0, buckets-1] — clamp the last edge into the final bucket
        let b = ((x - min) / range * (buckets - 1) as f64) as usize;
        counts[b.min(buckets - 1)] += 1;
    }

    let max_count = *counts.iter().max().unwrap_or(&1);

    println!("\n  Distribution ({} buckets):", buckets);
    for i in 0..buckets {
        let lo     = min + i as f64       * range / buckets as f64;
        let hi     = min + (i + 1) as f64 * range / buckets as f64;
        let filled = if max_count > 0 { counts[i] * bar_width / max_count } else { 0 };

        // Build the bar string: `filled` '#' characters padded to `bar_width`
        let bar: String = (0..bar_width)
            .map(|j| if j < filled { '#' } else { ' ' })
            .collect();

        println!("  {:>6.2}\u{2013}{:>6.2}  [{}]  {}", lo, hi, bar, counts[i]);
    }
    println!();
}

// ── Entry point ─────────────────────────────────────────────────────────────

fn main() {
    // Collect CLI arguments, skipping argv[0] (the binary name)
    let raw_args: Vec<String> = env::args().skip(1).collect();

    if raw_args.is_empty() {
        eprintln!("usage: stats <number> [<number>...]");
        process::exit(1);
    }

    // Parse each argument as f64; report the first bad value and exit
    let data: Vec<f64> = raw_args
        .iter()
        .map(|s| {
            s.parse::<f64>().unwrap_or_else(|_| {
                eprintln!("error: not a number: {}", s);
                process::exit(1);
            })
        })
        .collect();

    let s = compute(&data).expect("data is non-empty — checked above");

    // ── Summary table ────────────────────────────────────────────────────────
    let sep = "\u{2500}".repeat(29); // ─────────────────────────────
    println!("{}", sep);
    println!("  n      {}", s.n);
    println!("  sum    {:>10.4}", s.sum);
    println!("  mean   {:>10.4}", s.mean);
    println!("  std    {:>10.4}", s.std_dev);
    println!("  min    {:>10.4}", s.min);
    println!("  max    {:>10.4}", s.max);
    println!("{}", sep);

    // ── Histogram ────────────────────────────────────────────────────────────
    print_histogram(&data, s.min, s.max, 8, 20);
}

// ── Unit tests ───────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_empty() {
        assert!(compute(&[]).is_none());
    }

    #[test]
    fn test_single() {
        let s = compute(&[42.0]).unwrap();
        assert_eq!(s.n, 1);
        assert!((s.mean - 42.0).abs() < 1e-10);
        assert!((s.std_dev).abs() < 1e-10);
        assert_eq!(s.min, 42.0);
        assert_eq!(s.max, 42.0);
    }

    #[test]
    fn test_known_values() {
        // data: [2, 4, 4, 4, 5, 5, 7, 9]  —  mean=5, std_dev=2 (population)
        let data = vec![2.0, 4.0, 4.0, 4.0, 5.0, 5.0, 7.0, 9.0];
        let s    = compute(&data).unwrap();
        assert_eq!(s.n, 8);
        assert!((s.mean    - 5.0).abs() < 1e-10);
        assert!((s.std_dev - 2.0).abs() < 1e-10);
        assert_eq!(s.min, 2.0);
        assert_eq!(s.max, 9.0);
    }
}
