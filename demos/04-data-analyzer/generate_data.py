"""Generate sample CSV sales data for the analyzer demo."""
import csv
import random
from pathlib import Path

PRODUCTS = ["Widget A", "Widget B", "Gadget X", "Gadget Y", "Tool Z"]
REGIONS = ["North", "South", "East", "West"]
MONTHS = [f"2024-{m:02d}" for m in range(1, 13)]

random.seed(42)

rows = []
for month in MONTHS:
    for region in REGIONS:
        for product in PRODUCTS:
            rows.append({
                "month": month,
                "region": region,
                "product": product,
                "units": random.randint(10, 500),
                "unit_price": round(random.uniform(5.0, 150.0), 2),
            })

out = Path("data/sales.csv")
out.parent.mkdir(exist_ok=True)
with out.open("w", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=list(rows[0].keys()))
    writer.writeheader()
    writer.writerows(rows)

print(f"Generated {len(rows)} rows → {out}")
