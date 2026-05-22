# Contributing to molt

Thank you for your interest in contributing! This document covers how to set up a development environment, run tests, and submit changes.

---

## Development setup

**Prerequisites:** Go 1.22+

```bash
git clone https://github.com/luowensheng/molt-python.git
cd molt-python
go build -o molt .
./molt --version
```

That's it. molt has only two external Go dependencies (`gopkg.in/yaml.v3` and `github.com/bmatcuk/doublestar/v4`), so `go build` is essentially instant.

### Useful make targets

```bash
make build        # build molt binary
make test         # run all tests
make lint         # go fmt + go vet
make release-dry  # cross-compile to dist/ without publishing
```

---

## Running tests

```bash
go test ./...                      # unit tests
go test -tags integration ./...    # integration tests (requires built molt binary)
```

The integration tests start the MCP server and run a few smoke checks against the built binary. They are gated behind the `integration` build tag so they don't run in environments without a built binary.

---

## Project layout

```
main.go                    # CLI entry point; all subcommands dispatched here
internal/
  mcpserver/               # MCP stdio server (molt mcp)
  syncplan/                # dependency resolution + shim writing
  syspath/                 # PYTHONPATH / env spec
  native/                  # Cython, Rust, kernel module builders
  kernelbuilder/           # per-language compile recipes
  python/                  # Python version management
  tasks/                   # task runner
  store/                   # global package store
  projstate/               # per-project state directory (~/.molt/projects/<id>/)
  moltcfg/                 # pyproject.toml / molt.yaml parsing
  builder/                 # single-binary distribution
  tooldb/                  # global CLI tool registry
  ... (25 more packages)
embedder/                  # binary payload packing
demos/                     # 12 runnable examples
docs/                      # reference documentation
```

---

## Submitting a change

1. **Fork** the repository and create a branch: `git checkout -b my-feature`
2. **Write tests** for any new behaviour (unit or integration)
3. **Run** `make lint` and `make test` before pushing
4. **Update `CHANGELOG.md`** under `## [Unreleased]` — even one line is enough
5. Open a **pull request** against `main`

### Commit message style

```
<type>(<scope>): <short summary>

Types: feat, fix, docs, test, refactor, chore
Scope: optional — e.g. mcp, syncplan, python, native, demos

Examples:
  feat(mcp): add molt_exec tool
  fix(syncplan): handle Windows path separators in shim
  docs: add Homebrew install instructions
```

---

## Reporting bugs

Please use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.md). Include:
- `molt --version` output
- OS and architecture
- Minimal reproduction steps
- Full error output

---

## Code style

- Standard Go formatting (`go fmt`)
- No `panic` in library code — return errors
- Keep `main.go` additions focused: new commands get their own `cmd*` function
- Prefer adding a new `internal/` package over growing an existing one beyond ~600 lines

---

## License

By contributing you agree that your contributions will be licensed under the [Apache 2.0 License](LICENSE).
