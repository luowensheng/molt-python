"""
sysinfo — system info reporter.
Demonstrates molt build: packages source + deps into a self-installing binary.
Multiple commands (info, bench, version) are defined in molt.yaml.
"""
import sys
import platform
import time
import os
import shutil

import click
from rich.console import Console
from rich.table import Table
from rich.panel import Panel

console = Console()


@click.group()
def cli():
    """System info utility — molt build demo."""


@cli.command()
def info():
    """Display system information."""
    table = Table(show_header=False, box=None, padding=(0, 2))
    table.add_column("Key", style="bold cyan")
    table.add_column("Value")

    rows = [
        ("Python", sys.version.split()[0]),
        ("Platform", platform.platform()),
        ("Machine", platform.machine()),
        ("Processor", platform.processor() or "n/a"),
        ("CPUs", str(os.cpu_count())),
        ("CWD", os.getcwd()),
        ("PATH entries", str(len(os.environ.get("PATH", "").split(":")))),
    ]
    for k, v in rows:
        table.add_row(k, v)

    disk = shutil.disk_usage("/")
    table.add_row("Disk free", _fmt_size(disk.free))
    table.add_row("Disk total", _fmt_size(disk.total))

    console.print(Panel(table, title="System Info", expand=False))


@cli.command()
@click.option("--n", default=10_000_000, show_default=True, help="Iterations")
def bench(n: int):
    """Run a simple Python loop benchmark."""
    console.print(f"Benchmarking {n:,} iterations...")
    start = time.perf_counter()
    total = sum(range(n))
    elapsed = time.perf_counter() - start
    mops = n / elapsed / 1_000_000

    console.print(Panel(
        f"[bold]Sum     :[/bold] {total:,}\n"
        f"[bold]Time    :[/bold] {elapsed:.3f}s\n"
        f"[bold]Rate    :[/bold] {mops:.1f}M ops/s",
        title="Benchmark",
        expand=False,
    ))


@cli.command()
def version():
    """Print version information."""
    console.print("[bold cyan]sysinfo[/bold cyan] v0.1.0")
    console.print(f"Built with Python {sys.version.split()[0]}")


def _fmt_size(n: int) -> str:
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.1f} {unit}"
        n //= 1024
    return f"{n} PB"


if __name__ == "__main__":
    cli()
