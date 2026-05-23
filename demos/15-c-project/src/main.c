/* main.c — statistics CLI tool
 * Usage: stats <number> [<number>...]
 *
 * Demonstrates a multi-file C project managed entirely by molt.
 * Build:  molt run build
 * Run:    molt run demo
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "stats.h"

static void bar(double value, double max, int width) {
    int filled = (int)((value / max) * width);
    putchar('[');
    for (int i = 0; i < width; i++) putchar(i < filled ? '#' : ' ');
    putchar(']');
}

int main(int argc, char *argv[]) {
    if (argc < 2) {
        fprintf(stderr, "usage: stats <number> [<number>...]\n");
        fprintf(stderr, "example: stats 1 2 3 4 5\n");
        return 1;
    }

    int n = argc - 1;
    double *data = malloc(n * sizeof(double));
    if (!data) { perror("malloc"); return 1; }

    for (int i = 0; i < n; i++)
        data[i] = atof(argv[i + 1]);

    double mean = stats_mean(data, n);
    double std  = stats_std(data, n);
    double mn   = stats_min(data, n);
    double mx   = stats_max(data, n);
    double sum  = stats_sum(data, n);

    printf("─────────────────────────────\n");
    printf("  n      %d\n", n);
    printf("  sum    %10.4f\n", sum);
    printf("  mean   %10.4f\n", mean);
    printf("  std    %10.4f\n", std);
    printf("  min    %10.4f\n", mn);
    printf("  max    %10.4f\n", mx);
    printf("─────────────────────────────\n");

    /* ASCII histogram — bucket each value between min and max */
    int buckets = 8;
    int *counts = calloc(buckets, sizeof(int));
    double range = mx - mn;
    if (range > 0) {
        for (int i = 0; i < n; i++) {
            int b = (int)((data[i] - mn) / range * (buckets - 1));
            counts[b]++;
        }
        int maxcount = 0;
        for (int i = 0; i < buckets; i++)
            if (counts[i] > maxcount) maxcount = counts[i];

        printf("\n  Distribution (%d buckets):\n", buckets);
        for (int i = 0; i < buckets; i++) {
            double lo = mn + i * range / buckets;
            double hi = mn + (i + 1) * range / buckets;
            printf("  %6.2f–%6.2f  ", lo, hi);
            bar(counts[i], maxcount, 20);
            printf("  %d\n", counts[i]);
        }
        printf("\n");
    }

    free(counts);
    free(data);
    return 0;
}
