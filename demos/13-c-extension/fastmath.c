#include "fastmath.h"
#include <math.h>

double add(double a, double b) {
    return a + b;
}

double mul(double a, double b) {
    return a * b;
}

double clamp(double value, double lo, double hi) {
    if (value < lo) return lo;
    if (value > hi) return hi;
    return value;
}

double lerp(double a, double b, double t) {
    return a + t * (b - a);
}

int gcd(int a, int b) {
    while (b != 0) {
        int tmp = b;
        b = a % b;
        a = tmp;
    }
    return a < 0 ? -a : a;
}

int ipow(int base, int exp) {
    int result = 1;
    while (exp > 0) {
        if (exp & 1) result *= base;
        base *= base;
        exp >>= 1;
    }
    return result;
}

double mean3(double a, double b, double c) {
    return (a + b + c) / 3.0;
}

double variance3(double a, double b, double c) {
    double m = mean3(a, b, c);
    double d0 = a - m;
    double d1 = b - m;
    double d2 = c - m;
    return (d0*d0 + d1*d1 + d2*d2) / 3.0;
}
