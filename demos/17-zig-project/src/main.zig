// main.zig — statistics CLI in Zig 0.16.
// Demonstrates: error unions, defer, explicit allocators, @import modules.
//
// Build: molt run build   (zig build-exe src/main.zig → bin/stats-zig)
// Run:   molt run demo
const std   = @import("std");
const stats = @import("stats.zig");

pub fn main(init: std.process.Init) !void {
    const arena = init.arena.allocator();
    const args  = try init.minimal.args.toSlice(arena);

    if (args.len < 2) {
        try std.Io.File.stderr().writeStreamingAll(
            init.io, "usage: stats-zig <number> [<number>...]\n");
        std.process.exit(1);
    }

    // Zig 0.16: ArrayList uses .empty sentinel, allocator passed per-call.
    var data: std.ArrayList(f64) = .empty;
    for (args[1..]) |arg| {
        const v = std.fmt.parseFloat(f64, arg) catch {
            const emsg = try std.fmt.allocPrint(
                arena, "error: not a number: {s}\n", .{arg});
            try std.Io.File.stderr().writeStreamingAll(init.io, emsg);
            std.process.exit(1);
        };
        try data.append(arena, v);
    }

    const r = try stats.compute(data.items);
    const out = std.Io.File.stdout();

    const sep = "\xe2\x94\x80" ** 29 ++ "\n"; // ─────────────────────────────
    try out.writeStreamingAll(init.io, sep);
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  n      {d}\n",       .{r.n}));
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  sum    {d:>10.4}\n", .{r.sum}));
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  mean   {d:>10.4}\n", .{r.mean}));
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  std    {d:>10.4}\n", .{r.std_dev}));
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  min    {d:>10.4}\n", .{r.min}));
    try out.writeStreamingAll(init.io,
        try std.fmt.allocPrint(arena, "  max    {d:>10.4}\n", .{r.max}));
    try out.writeStreamingAll(init.io, sep);

    // ASCII histogram.
    const buckets: usize = 8;
    var counts = [_]usize{0} ** buckets;
    const range = r.max - r.min;
    if (range > 0) {
        for (data.items) |x| {
            const b: usize = @intFromFloat(
                (x - r.min) / range * @as(f64, @floatFromInt(buckets - 1)));
            counts[@min(b, buckets - 1)] += 1;
        }
        var maxcount: usize = 0;
        for (counts) |c| if (c > maxcount) { maxcount = c; };

        try out.writeStreamingAll(init.io,
            try std.fmt.allocPrint(arena, "\n  Distribution ({d} buckets):\n", .{buckets}));

        for (0..buckets) |i| {
            const lo = r.min + @as(f64, @floatFromInt(i))
                * range / @as(f64, @floatFromInt(buckets));
            const hi = r.min + @as(f64, @floatFromInt(i + 1))
                * range / @as(f64, @floatFromInt(buckets));
            const filled: usize = if (maxcount > 0) counts[i] * 20 / maxcount else 0;

            // Build bar string: filled '#' then spaces.
            var bar: [20]u8 = [_]u8{' '} ** 20;
            for (0..filled) |j| bar[j] = '#';

            try out.writeStreamingAll(init.io,
                try std.fmt.allocPrint(arena,
                    "  {d:>6.2}\xe2\x80\x93{d:>6.2}  [{s}]  {d}\n",
                    .{ lo, hi, bar, counts[i] }));
        }
        try out.writeStreamingAll(init.io, "\n");
    }
}
