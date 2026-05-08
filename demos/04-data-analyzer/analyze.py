"""
Data analysis demo using pandas + rich tables.
Showcases: molt add pandas rich → packages pulled from global store, zero venv setup.
"""
import sys
from pathlib import Path

import pandas as pd
from rich.console import Console
from rich.table import Table
from rich.panel import Panel

console = Console()
DATA = Path("data/sales.csv")


def load() -> pd.DataFrame:
    if not DATA.exists():
        console.print("[red]data/sales.csv not found — run: molt run generate[/red]")
        sys.exit(1)
    df = pd.read_csv(DATA)
    df["revenue"] = df["units"] * df["unit_price"]
    return df


def top_products(df: pd.DataFrame, n: int = 5) -> None:
    console.print(Panel(f"[bold cyan]Top {n} Products by Revenue[/bold cyan]", expand=False))
    top = (
        df.groupby("product")["revenue"]
        .sum()
        .sort_values(ascending=False)
        .head(n)
        .reset_index()
    )
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("Rank", style="dim", width=5)
    table.add_column("Product")
    table.add_column("Revenue", justify="right")
    for i, row in top.iterrows():
        table.add_row(str(i + 1), row["product"], f"${row['revenue']:,.2f}")
    console.print(table)


def by_region(df: pd.DataFrame) -> None:
    console.print(Panel("[bold cyan]Revenue by Region[/bold cyan]", expand=False))
    region = (
        df.groupby("region")["revenue"]
        .sum()
        .sort_values(ascending=False)
        .reset_index()
    )
    table = Table(show_header=True, header_style="bold green")
    table.add_column("Region")
    table.add_column("Revenue", justify="right")
    table.add_column("% of Total", justify="right")
    total = region["revenue"].sum()
    for _, row in region.iterrows():
        pct = row["revenue"] / total * 100
        table.add_row(row["region"], f"${row['revenue']:,.2f}", f"{pct:.1f}%")
    console.print(table)


def monthly_trend(df: pd.DataFrame) -> None:
    console.print(Panel("[bold cyan]Monthly Revenue Trend[/bold cyan]", expand=False))
    monthly = df.groupby("month")["revenue"].sum().reset_index()
    max_rev = monthly["revenue"].max()
    table = Table(show_header=True, header_style="bold yellow")
    table.add_column("Month")
    table.add_column("Revenue", justify="right")
    table.add_column("Bar")
    for _, row in monthly.iterrows():
        bar_len = int(row["revenue"] / max_rev * 30)
        bar = "█" * bar_len
        table.add_row(row["month"], f"${row['revenue']:,.2f}", f"[green]{bar}[/green]")
    console.print(table)


def summary(df: pd.DataFrame) -> None:
    total = df["revenue"].sum()
    rows = len(df)
    products = df["product"].nunique()
    regions = df["region"].nunique()
    console.print(Panel(
        f"[bold]Total revenue:[/bold] ${total:,.2f}\n"
        f"[bold]Records      :[/bold] {rows}\n"
        f"[bold]Products     :[/bold] {products}\n"
        f"[bold]Regions      :[/bold] {regions}",
        title="Summary",
        expand=False,
    ))


COMMANDS = {"top": top_products, "region": by_region, "trend": monthly_trend, "summary": summary}


def main() -> None:
    cmd = sys.argv[1] if len(sys.argv) > 1 else "summary"
    if cmd not in COMMANDS:
        console.print(f"[red]Unknown command:[/red] {cmd!r}")
        console.print(f"Available: {', '.join(COMMANDS)}")
        sys.exit(1)
    df = load()
    COMMANDS[cmd](df)


if __name__ == "__main__":
    main()
