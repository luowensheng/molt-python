// hello.zig — single-file Zig program (Zig 0.16 API)
// Run directly with: molt run hello.zig [name]
// molt resolves {zig} to the auto-installed zig binary.
const std = @import("std");

pub fn main(init: std.process.Init) !void {
    const arena = init.arena.allocator();
    const args = try init.minimal.args.toSlice(arena);
    const name: []const u8 = if (args.len > 1) args[1] else "World";

    var buf: [256]u8 = undefined;
    const msg = try std.fmt.bufPrint(&buf, "Hello from Zig {s}! \xf0\x9f\x91\x8b {s}\n",
        .{ @import("builtin").zig_version_string, name });
    try std.Io.File.stdout().writeStreamingAll(init.io, msg);
}
