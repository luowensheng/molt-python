/* stats.c — descriptive statistics implementation
 * Plain C, no Python awareness. Compiled and managed by molt.
 */
#include "stats.h"
#include <math.h>
#include <float.h>

double stats_sum(const double *data, int n) {
    double s = 0.0;
    for (int i = 0; i < n; i++) s += data[i];
    return s;
}

double stats_mean(const double *data, int n) {
    return stats_sum(data, n) / n;
}

double stats_std(const double *data, int n) {
    double m = stats_mean(data, n), v = 0.0;
    for (int i = 0; i < n; i++) {
        double d = data[i] - m;
        v += d * d;
    }
    return sqrt(v / n);
}

double stats_min(const double *data, int n) {
    double r = DBL_MAX;
    for (int i = 0; i < n; i++) if (data[i] < r) r = data[i];
    return r;
}

double stats_max(const double *data, int n) {
    double r = -DBL_MAX;
    for (int i = 0; i < n; i++) if (data[i] > r) r = data[i];
    return r;
}
