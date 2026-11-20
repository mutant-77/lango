package pty

import "time"

const (
	BusyWindow = 2.0
	EchoWindow = 0.3
)

// Clock provides monotonic time in seconds.
type Clock interface {
	Now() float64
}

var monotonicStart = time.Now()

type realClock struct{}

func (realClock) Now() float64 {
	return time.Since(monotonicStart).Seconds()
}

// NewRealClock returns a default monotonic Clock.
func NewRealClock() Clock {
	return realClock{}
}

// ComputeState computes session state from activity and agent status.
func ComputeState(alive bool, agent string, used bool, lastOutput, now float64) string {
	if !alive {
		return "fresh"
	}
	if agent != "" {
		if now-lastOutput < BusyWindow {
			return "busy"
		}
		return "waiting"
	}
	if used {
		return "used"
	}
	return "fresh"
}
