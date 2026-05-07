// Package signals provides reusable signal calculators for strategies that
// need to compose a market scalar (the "Signal" plugged into Sigmoid or any
// other decision function).
//
// Every implementation is a pure function over a closes-only series. Strategies
// pick one or more signals and compose them with their own weights (typically
// chromosome-evolved).
//
// Iron rules:
//   - Pure: no I/O, no clock, no rand
//   - Deterministic: same input → same output
//   - Returns NaN for insufficient data so the caller can detect warmup
package signals

// Signal computes a single dimensionless market scalar from a close series.
type Signal interface {
	// Value returns the signal at the latest sample. NaN if input is too short
	// or the calculation is undefined (e.g. division by zero variance).
	Value(closes []float64) float64
}
