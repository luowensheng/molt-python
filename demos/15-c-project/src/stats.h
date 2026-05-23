#ifndef STATS_H
#define STATS_H

/* Descriptive statistics on an array of doubles. */
double stats_mean(const double *data, int n);
double stats_std (const double *data, int n);
double stats_min (const double *data, int n);
double stats_max (const double *data, int n);
double stats_sum (const double *data, int n);

#endif /* STATS_H */
