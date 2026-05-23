// main.cpp — statistics CLI using C++17 features.
// Build with: molt run build
// Run  with:  molt run stats  (or bin/stats-cpp <numbers...>)
#include <iostream>
#include <sstream>
#include <vector>
#include <string>
#include <iomanip>
#include "vec.hpp"

static void bar(double value, double max, int width) {
    int filled = max > 0 ? (int)(value / max * width) : 0;
    std::cout << '[';
    for (int i = 0; i < width; ++i)
        std::cout << (i < filled ? '#' : ' ');
    std::cout << ']';
}

int main(int argc, char* argv[]) {
    if (argc < 2) {
        std::cerr << "usage: stats-cpp <number> [<number>...]\n";
        return 1;
    }

    std::vector<double> data;
    for (int i = 1; i < argc; ++i)
        data.push_back(std::stod(argv[i]));

    auto [mean, sd, mn, mx, sum, n] = compute(data);

    std::cout << std::fixed << std::setprecision(4);
    std::cout << "─────────────────────────────\n";
    std::cout << "  n      " << n                            << "\n";
    std::cout << "  sum    " << std::setw(10) << sum         << "\n";
    std::cout << "  mean   " << std::setw(10) << mean        << "\n";
    std::cout << "  std    " << std::setw(10) << sd          << "\n";
    std::cout << "  min    " << std::setw(10) << mn          << "\n";
    std::cout << "  max    " << std::setw(10) << mx          << "\n";
    std::cout << "─────────────────────────────\n";

    // ASCII histogram.
    constexpr int buckets = 8;
    std::vector<int> counts(buckets, 0);
    double range = mx - mn;
    if (range > 0) {
        for (auto x : data) {
            int b = (int)((x - mn) / range * (buckets - 1));
            ++counts[b];
        }
        int maxcount = *std::max_element(counts.begin(), counts.end());
        std::cout << "\n  Distribution (" << buckets << " buckets):\n";
        for (int i = 0; i < buckets; ++i) {
            double lo = mn + i * range / buckets;
            double hi = mn + (i + 1) * range / buckets;
            std::cout << "  " << std::setw(6) << lo << "–"
                      << std::setw(6) << hi << "  ";
            bar(counts[i], maxcount, 20);
            std::cout << "  " << counts[i] << "\n";
        }
        std::cout << "\n";
    }
    return 0;
}
