# Demo 16 — C++17 as a Primary Language

This demo shows **C++17 as a first-class language** in a molt project.
molt uses `zig c++` as a zero-install C++17 compiler — no Xcode, no Homebrew,
no system toolchain required.

## What it demonstrates

- Single-file C++ scripts: `molt run hello.cpp`
- Multi-file C++17 projects managed with molt tasks
- C++17 features: templates, structured bindings, `std::minmax_element`
- Python driving the compiled C++ binary via `subprocess`

## Run it

```bash
# Single-file — compile-and-run in one command (no pre-build step)
molt run hello.cpp World

# Multi-file project
molt run build          # zig c++ -O2 -std=c++17 → bin/stats-cpp
molt run stats 88 92 71 95 84 76

# Python-orchestrated demo
molt run demo
```

## How single-file dispatch works

The `.cpp` run-handler compiles and runs in a single shell invocation:

```
zig c++ -O2 -std=c++17 -o {dir}/{basename} {file} && {dir}/{basename} {args}
```

`{zig}` resolves to the auto-installed zig binary (`~/.molt/toolchains/zig/`),
so the compiler is available on any machine without any manual installation.

## Project layout

```
hello.cpp          # single-file demo — molt run hello.cpp
src/
  vec.hpp          # generic Stats<T> template (C++17)
  main.cpp         # CLI: parse args, compute stats, print histogram
bin/
  stats-cpp        # compiled output (gitignored)
demo.py            # Python driver: runs the binary with three datasets
pyproject.toml     # build / stats / demo / clean tasks
```

## pyproject.toml tasks

```toml
[tool.molt.tasks]
build = "zig c++ -O2 -std=c++17 -o bin/stats-cpp src/main.cpp && echo '✓ compiled bin/stats-cpp'"
stats = "bin/stats-cpp"
demo  = "python demo.py"
clean = "rm -f bin/stats-cpp bin/stats-cpp.exe"
```

## Key code — `src/vec.hpp`

```cpp
template<typename T>
Stats<T> compute(const std::vector<T>& v) {
    double s   = std::accumulate(v.begin(), v.end(), 0.0);
    double m   = s / v.size();
    double var = 0.0;
    for (auto x : v) { double d = x - m; var += d * d; }
    auto [lo, hi] = std::minmax_element(v.begin(), v.end()); // C++17 structured binding
    return { m, std::sqrt(var / v.size()), (double)*lo, (double)*hi, s, v.size() };
}
```
