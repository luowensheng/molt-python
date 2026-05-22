# helpers.py
# Local Python module imported from Mojo via Python.add_to_path(".") +
# Python.import_module("helpers").
#
# Demonstrates that Mojo can call any Python code — not just PyPI packages.

import numpy as np


def normalize_scores(scores: list[float]) -> list[float]:
    """Min-max normalize a list of scores to [0, 1]."""
    arr = np.array(scores, dtype=float)
    lo, hi = arr.min(), arr.max()
    if hi == lo:
        return [0.0] * len(scores)
    return ((arr - lo) / (hi - lo)).tolist()


def moving_average(values: list[float], window: int = 3) -> list[float]:
    """Simple unweighted moving average."""
    arr = np.array(values, dtype=float)
    result = np.convolve(arr, np.ones(window) / window, mode="valid")
    return result.tolist()
