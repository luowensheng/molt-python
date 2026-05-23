// stats.zig — descriptive statistics module.
// Demonstrates Zig idioms: error unions, explicit types, no hidden allocations.
const std = @import("std");

pub const StatsResult = struct {
    n:       usize,
    sum:     f64,
    mean:    f64,
    std_dev: f64,
    min:     f64,
    max:     f64,
};

pub fn compute(data: []const f64) error{EmptySlice}!StatsResult {
    if (data.len == 0) return error.EmptySlice;

    var sum: f64 = 0;
    var mn:  f64 = data[0];
    var mx:  f64 = data[0];
    for (data) |x| {
        sum += x;
        if (x < mn) mn = x;
        if (x > mx) mx = x;
    }
    const n    = @as(f64, @floatFromInt(data.len));
    const mean = sum / n;

    var variance: f64 = 0;
    for (data) |x| {
        const d = x - mean;
        variance += d * d;
    }
    const std_dev = std.math.sqrt(variance / n);

    return .{
        .n       = data.len,
        .sum     = sum,
        .mean    = mean,
        .std_dev = std_dev,
        .min     = mn,
        .max     = mx,
    };
}
