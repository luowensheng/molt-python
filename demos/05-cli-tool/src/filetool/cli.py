"""
filetool — a simple file utility CLI built with Click.
Demo: molt tool install registers it as a global shim in ~/.molt/bin/filetool
"""
import hashlib
import os
from pathlib import Path

import click
from rich.console import Console
from rich.table import Table
from rich.panel import Panel

console = Console()


@click.group()
@click.version_option("0.1.0", prog_name="filetool")
def cli():
    """File utilities — molt tool install demo."""


@cli.command()
@click.argument("directory", default=".", type=click.Path(exists=True))
@click.option("--ext", default=None, help="Filter by extension (e.g. .py)")
@click.option("--sort", type=click.Choice(["name", "size", "mtime"]), default="name")
def ls(directory: str, ext: str | None, sort: str):
    """List files in DIRECTORY with size and modification time."""
    path = Path(directory)
    entries = [p for p in path.iterdir() if p.is_file()]
    if ext:
        entries = [p for p in entries if p.suffix == ext]

    key_fn = {
        "name": lambda p: p.name,
        "size": lambda p: p.stat().st_size,
        "mtime": lambda p: p.stat().st_mtime,
    }[sort]
    entries.sort(key=key_fn)

    table = Table(show_header=True, header_style="bold cyan")
    table.add_column("Name")
    table.add_column("Size", justify="right")
    table.add_column("Modified")
    import datetime
    for p in entries:
        stat = p.stat()
        size = _fmt_size(stat.st_size)
        mtime = datetime.datetime.fromtimestamp(stat.st_mtime).strftime("%Y-%m-%d %H:%M")
        table.add_row(p.name, size, mtime)

    console.print(Panel(f"[bold]{path.resolve()}[/bold]", expand=False))
    console.print(table)
    console.print(f"[dim]{len(entries)} file(s)[/dim]")


@cli.command()
@click.argument("file", type=click.Path(exists=True))
@click.option("--algo", default="sha256", type=click.Choice(["md5", "sha1", "sha256", "sha512"]))
def checksum(file: str, algo: str):
    """Compute a checksum for FILE."""
    h = hashlib.new(algo)
    data = Path(file).read_bytes()
    h.update(data)
    digest = h.hexdigest()
    console.print(f"[bold]{algo}[/bold]  [green]{digest}[/green]  {file}")


@cli.command()
@click.argument("directory", default=".", type=click.Path(exists=True))
@click.option("--ext", default=None, help="Count only files with this extension")
def stats(directory: str, ext: str | None):
    """Show file stats for DIRECTORY (recursive)."""
    path = Path(directory)
    files = [p for p in path.rglob("*") if p.is_file()]
    if ext:
        files = [p for p in files if p.suffix == ext]

    total_size = sum(p.stat().st_size for p in files)
    by_ext: dict[str, int] = {}
    for p in files:
        by_ext[p.suffix or "(none)"] = by_ext.get(p.suffix or "(none)", 0) + 1

    console.print(Panel(
        f"[bold]Files    :[/bold] {len(files)}\n"
        f"[bold]Total size:[/bold] {_fmt_size(total_size)}",
        title=f"Stats: {path}",
        expand=False,
    ))

    if by_ext:
        table = Table(show_header=True, header_style="bold yellow")
        table.add_column("Extension")
        table.add_column("Count", justify="right")
        for ext_key, count in sorted(by_ext.items(), key=lambda x: -x[1]):
            table.add_row(ext_key, str(count))
        console.print(table)


def _fmt_size(n: int) -> str:
    for unit in ("B", "KB", "MB", "GB"):
        if n < 1024:
            return f"{n:.1f} {unit}"
        n /= 1024
    return f"{n:.1f} TB"
