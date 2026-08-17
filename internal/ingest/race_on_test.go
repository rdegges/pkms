//go:build race

package ingest

// raceDetectorEnabled reports whether this test binary carries race
// instrumentation (the `race` build tag is set by `go test -race`).
const raceDetectorEnabled = true
