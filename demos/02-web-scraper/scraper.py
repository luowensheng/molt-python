"""
Demo: fetch public JSON APIs and display results with rich.
Shows molt add (requests, rich) → zero venv setup, shared global store.
"""
import sys
import requests
from rich.console import Console
from rich.table import Table
from rich.panel import Panel
from rich import print as rprint

console = Console()


def fetch_posts(limit: int = 10) -> list[dict]:
    url = "https://jsonplaceholder.typicode.com/posts"
    resp = requests.get(url, timeout=10)
    resp.raise_for_status()
    return resp.json()[:limit]


def fetch_users() -> list[dict]:
    url = "https://jsonplaceholder.typicode.com/users"
    resp = requests.get(url, timeout=10)
    resp.raise_for_status()
    return resp.json()


def show_posts(limit: int = 5) -> None:
    console.print(Panel("[bold cyan]Recent Posts[/bold cyan]", expand=False))
    posts = fetch_posts(limit)
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Title")
    table.add_column("User", width=8)
    for post in posts:
        table.add_row(str(post["id"]), post["title"][:60], str(post["userId"]))
    console.print(table)


def show_users() -> None:
    console.print(Panel("[bold cyan]Users[/bold cyan]", expand=False))
    users = fetch_users()
    table = Table(show_header=True, header_style="bold green")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Name")
    table.add_column("Email")
    table.add_column("City")
    for u in users:
        table.add_row(
            str(u["id"]),
            u["name"],
            u["email"],
            u["address"]["city"],
        )
    console.print(table)


def main() -> None:
    cmd = sys.argv[1] if len(sys.argv) > 1 else "posts"
    limit = int(sys.argv[2]) if len(sys.argv) > 2 else 5

    if cmd == "posts":
        show_posts(limit)
    elif cmd == "users":
        show_users()
    else:
        rprint(f"[red]Unknown command:[/red] {cmd!r}. Try: posts, users")
        sys.exit(1)


if __name__ == "__main__":
    main()
