// Package stats provides high-performance statistical functions implemented
// in Go and callable from Python via molt's transport glue.
//
// Note: this file is "package stats", NOT "package main". molt's generated
// server imports it as a proper Go package via a replace directive in go.mod.
package stats

import (
	"bytes"
	"compress/zlib"
	"math"
)

// Mean returns the arithmetic mean of data. Returns 0 for empty input.
func Mean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

// Stddev returns the population standard deviation of data.
func Stddev(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	m := Mean(data)
	v := 0.0
	for _, x := range data {
		d := x - m
		v += d * d
	}
	return math.Sqrt(v / float64(len(data)))
}

// Histogram returns a slice of bucket counts for data using the given number
// of equal-width buckets spanning the data's range.
func Histogram(data []float64, buckets int32) []int32 {
	counts := make([]int32, buckets)
	if len(data) == 0 || buckets <= 0 {
		return counts
	}
	min, max := data[0], data[0]
	for _, v := range data {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min == max {
		counts[0] = int32(len(data))
		return counts
	}
	span := max - min
	for _, v := range data {
		idx := int((v-min)/span * float64(buckets-1))
		if idx >= int(buckets) {
			idx = int(buckets) - 1
		}
		counts[idx]++
	}
	return counts
}

// Compress compresses payload with zlib and returns the compressed bytes.
func Compress(payload []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(payload)
	_ = w.Close()
	return buf.Bytes()
}
