# 14-polyglot

**What this shows:** `molt run` as a universal script launcher. Any file whose
extension is registered in `~/.molt/run-handlers.yaml` is dispatched directly —
no tasks, no configuration, no activation.

```bash
molt run hello.rb    # → ruby hello.rb
molt run hello.js    # → node hello.js
molt run hello.go    # → go run hello.go
molt run hello.jl    # → julia hello.jl
molt run hello.exs   # → elixir hello.exs
```

---

## What's in this demo

```
14-polyglot/
  hello.rb     Ruby greeting
  hello.js     Node.js greeting
  hello.lua    Lua greeting
  hello.pl     Perl greeting
  hello.sh     Bash greeting
  hello.go     Go greeting
  hello.swift  Swift greeting
  hello.jl     Julia greeting
  hello.exs    Elixir greeting
  showcase.py  Python runner that executes and times each script
  pyproject.toml
```

---

## Running

```bash
# Run any individual script directly
molt run hello.rb               # Hello from Ruby 2.6.10! 👋 World
molt run hello.rb Alice         # Hello from Ruby 2.6.10! 👋 Alice
molt run hello.js Bob
molt run hello.go Charlie
molt run hello.jl               # Hello from Julia 1.11.4! 👋 World

# Run the polyglot showcase (all languages in a table)
molt run all

# Or filter to one language
molt run julia
```

---

## How dispatch works

`molt run <arg>` checks, in order:

1. Is it `--` or a flag-shaped arg? → default entry
2. No arg? → `main.py` or `python -m <pkg>`
3. Ends in `.py` and file exists? → `runPythonScript`
4. Ends in `.mojo`/`.🔥` and file exists? → `mojo run`
5. **Has any extension, file exists, and handler registered?** → `runWithHandler` ← this demo
6. Matches a task in `[tool.molt.tasks]`? → task runner
7. Fallthrough → exec binary on PATH

---

## Managing handlers

```bash
# See all built-in and user handlers
molt run-handler list

# Show command for a specific extension
molt run-handler show rb
# Extension : .rb
# Command   : ruby {file} {args}

# Register a custom handler globally
molt run-handler add deno "deno run {file} {args}"

# Register with a Windows platform override
molt run-handler add ts "npx ts-node {file} {args}" --windows "npx.cmd ts-node {file} {args}"

# Remove a custom handler (built-ins cannot be removed, only overridden)
molt run-handler remove deno

# Restore factory defaults
molt run-handler reset

# List all built-in command templates
molt run-handler templates
```

---

## Built-in handlers (25)

| Extension   | Runtime         | Command                              |
|-------------|-----------------|--------------------------------------|
| `.rb`       | Ruby            | `ruby {file} {args}`                 |
| `.js`       | Node.js         | `node {file} {args}`                 |
| `.ts`       | ts-node         | `npx ts-node {file} {args}`          |
| `.lua`      | Lua             | `lua {file} {args}`                  |
| `.sh`/`.bash`| Bash           | `bash {file} {args}`                 |
| `.pl`       | Perl            | `perl {file} {args}`                 |
| `.r`        | R               | `Rscript {file} {args}`              |
| `.php`      | PHP             | `php {file} {args}`                  |
| `.swift`    | Swift           | `swift {file} {args}`                |
| `.go`       | Go              | `go run {file} {args}`               |
| `.java`     | Java 11+        | `java {file} {args}`                 |
| `.kt`       | Kotlin          | `kotlinc-jvm -script {file} {args}`  |
| `.groovy`   | Groovy          | `groovy {file} {args}`               |
| `.ps1`      | PowerShell      | `pwsh -File {file} {args}` (Unix)    |
| `.nim`      | Nim             | `nim r {file} {args}`                |
| `.cr`       | Crystal         | `crystal run {file} {args}`          |
| `.jl`       | Julia           | `julia {file} {args}`                |
| `.ex`/`.exs`| Elixir          | `elixir {file} {args}`               |
| `.hs`       | Haskell         | `runghc {file} {args}`               |
| `.clj`      | Clojure         | `clojure {file} {args}`              |
| `.dart`     | Dart            | `dart run {file} {args}`             |
| `.v`        | Vlang           | `v run {file} {args}`                |
| `.odin`     | Odin            | `odin run {file} {args}`             |

Handlers are stored in `~/.molt/run-handlers.yaml`. User-added entries take
precedence over built-ins of the same extension.

---

## Token reference

| Token        | Value                                |
|--------------|--------------------------------------|
| `{file}`     | Absolute path to the file            |
| `{dir}`      | Directory containing the file        |
| `{basename}` | Filename without extension           |
| `{args}`     | Extra arguments space-joined         |
| `{python}`   | Project's pinned Python interpreter  |
| `{zig}`      | Auto-installed zig binary            |

The project's full molt environment (PYTHONPATH, .molt/bin/ on PATH) is applied
before exec — so `{python}` resolves to the project's pinned interpreter and any
installed packages are available to child processes.

---

## Cross-platform handlers

```bash
# Different command per OS
molt run-handler add ps1 \
  "pwsh -File {file} {args}" \
  --windows "powershell.exe -File {file} {args}"

# Check what command will be used on the current platform
molt run-handler show ps1
```

On Windows, `Command` is used as a fallback when no `--windows` override is set.
On Unix, `Command` is used as a fallback when no `--unix` override is set.

---

## Tested output

```
── Live test, confirmed passing ─────────────────────────────────────────
  molt run hello.rb    molt  →  Hello from Ruby 2.6.10! 👋 molt
  molt run hello.js    molt  →  Hello from Node.js v24.7.0! 👋 molt
  molt run hello.lua   molt  →  Hello from Lua 5.4! 👋 molt
  molt run hello.go    molt  →  Hello from Go go1.24.1! 👋 molt
  molt run hello.swift molt  →  Hello from Swift! 👋 molt
  molt run hello.jl    molt  →  Hello from Julia 1.11.4! 👋 molt
  molt run hello.exs   molt  →  Hello from Elixir 1.18.3! 👋 molt
─────────────────────────────────────────────────────────────────────────
```

All 9 languages confirmed. Args forwarding works on every handler.
