# Demo 17 — Zig as a Primary Language

This demo shows **Zig 0.16 as a first-class language** in a molt project.
molt resolves `{zig}` to the auto-installed Zig binary — no manual toolchain
installation required.

## What it demonstrates

- Single-file Zig scripts: `molt run hello.zig`
- Multi-file Zig projects built with `zig build-exe` via molt tasks
- Zig 0.16 idioms: error unions, explicit allocators, `std.process.Init`
- Python driving the compiled Zig binary via `subprocess`

## Run it

```bash
# Single-file — compile-and-run via the .zig run-handler
molt run hello.zig World

# Multi-file project
molt run build          # zig build-exe src/main.zig → bin/stats-zig
molt run stats 88 92 71 95 84 76

# Python-orchestrated demo
molt run demo
```

## How single-file dispatch works

The `.zig` run-handler invokes `zig run` directly — no compile step needed:

```
{zig} run {file} -- {args}
```

`{zig}` resolves to the auto-installed zig binary (`~/.molt/toolchains/zig/`).

## Project layout

```
hello.zig             # single-file demo — molt run hello.zig
src/
  stats.zig           # statistics module: StatsResult struct + compute()
  main.zig            # CLI: parse args, call stats.compute, print histogram
bin/
  stats-zig           # compiled output from `molt run build` (gitignored)
demo.py               # Python driver: runs the binary with three datasets
moltproject.toml      # build / stats / demo / clean tasks (no Python required)
```

## moltproject.toml tasks

This demo uses `moltproject.toml` — the config format for non-Python projects.
It has the same `[tool.molt.tasks]` format as `pyproject.toml` but without
`requires-python` or `dependencies`, so no Python sync step is needed.

```toml
[tool.molt.tasks]
build = "zig build-exe src/main.zig -femit-bin=bin/stats-zig && echo '✓ compiled bin/stats-zig'"
stats = "bin/stats-zig"
demo  = "python demo.py"
clean = "rm -f bin/stats-zig bin/stats-zig.exe bin/stats-zig.o"
```

## Key code — Zig 0.16 API

Zig 0.16 restructured the standard library. Key patterns used in this demo:

```zig
pub fn main(init: std.process.Init) !void {
    // Arena allocator from init — no manual allocator setup
    const arena = init.arena.allocator();
    const args = try init.minimal.args.toSlice(arena);

    // ArrayList: .empty sentinel, allocator passed per-call
    var data: std.ArrayList(f64) = .empty;
    try data.append(arena, value);

    // stdout via std.Io.File
    try std.Io.File.stdout().writeStreamingAll(init.io, bytes);
    try std.Io.File.stderr().writeStreamingAll(init.io, "error\n");
}
```

## Key code — `src/stats.zig`

```zig
pub const StatsResult = struct {
    n: usize, sum: f64, mean: f64, std_dev: f64, min: f64, max: f64,
};

// Error union: returns EmptySlice error or StatsResult
pub fn compute(data: []const f64) error{EmptySlice}!StatsResult {
    if (data.len == 0) return error.EmptySlice;
    // ... two-pass mean and variance computation
}
```
