#ifndef FASTMATH_H
#define FASTMATH_H

/* Scalar arithmetic */
double add(double a, double b);
double mul(double a, double b);
double clamp(double value, double lo, double hi);
double lerp(double a, double b, double t);

/* Integer ops */
int gcd(int a, int b);
int ipow(int base, int exp);

/* Stats on fixed-size inputs — passed as individual args */
double mean3(double a, double b, double c);
double variance3(double a, double b, double c);

#endif /* FASTMATH_H */
