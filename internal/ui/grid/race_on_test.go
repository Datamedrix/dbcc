//go:build race

package grid

// raceEnabled: the race detector slows drawing about tenfold, so frame-time
// limits are not meaningful in race builds (correctness is still tested).
const raceEnabled = true
