# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| latest (`main`) | ✅ |
| older releases | fixes backported on a case-by-case basis |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Use [GitHub's private vulnerability reporting](https://github.com/luowensheng/molt-python/security/advisories/new) to report security issues confidentially.

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce
- `molt --version` output and your OS/architecture

You will receive an acknowledgement within 72 hours. We aim to publish a fix and a public advisory within 14 days of confirmation.

## Scope

Security issues relevant to molt include:
- Arbitrary code execution via crafted `pyproject.toml` or `molt.yaml`
- Path traversal or privilege escalation in the build system
- Integrity bypass of embedded binary payloads (`molt verify-binary`)
- MCP server issues that could allow unintended host command execution

Out of scope: vulnerabilities in downstream tools (uv, pip, Mojo, Cython, Rust/cargo) — please report those to their respective projects.
