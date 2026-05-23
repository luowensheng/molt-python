#pragma once
// vec.hpp — generic statistics over any numeric container.
// Demonstrates C++17 templates, structured bindings, and STL algorithms.
#include <vector>
#include <numeric>
#include <algorithm>
#include <cmath>
#include <stdexcept>

template<typename T>
struct Stats {
    double mean, std_dev, min_val, max_val, sum;
    std::size_t n;
};

template<typename T>
Stats<T> compute(const std::vector<T>& v) {
    if (v.empty()) throw std::runtime_error("empty dataset");
    double s   = std::accumulate(v.begin(), v.end(), 0.0);
    double m   = s / v.size();
    double var = 0.0;
    for (auto x : v) { double d = x - m; var += d * d; }
    auto [lo, hi] = std::minmax_element(v.begin(), v.end());
    return { m, std::sqrt(var / v.size()), (double)*lo, (double)*hi, s, v.size() };
}
