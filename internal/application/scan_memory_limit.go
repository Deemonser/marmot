package application

import (
	"math"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"time"
)

const (
	// scanMemoryHeadroom is how much the runtime may hold above the live set
	// while a scan runs (ADR-0058 §1). It is a memory budget, not a tuning knob:
	// changing it needs a measurement, and R-059 §4.3 only swept it on one volume.
	scanMemoryHeadroom = 100 << 20
	// scanMemoryLimitInterval is how often the limit is recomputed. A scan
	// shorter than this gets no adjustment, which is harmless.
	scanMemoryLimitInterval = 100 * time.Millisecond
	// noMemoryLimit is what Go uses when GOMEMLIMIT is unset.
	noMemoryLimit = math.MaxInt64
)

// liveHeapBytes reports the heap occupied by objects the last GC marked live.
//
// It must be /gc/heap/live:bytes. /memory/classes/heap/objects:bytes looks like
// the same thing and is not: it also counts dead objects the sweeper has not
// reached yet, so right after a scan it reads roughly twice the real figure. A
// limit derived from it always sits above where the heap already is, which means
// it does nothing — and does nothing *silently*, looking exactly like a strategy
// that simply has no effect (R-059 §3).
func liveHeapBytes() int64 {
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(sample[0].Value.Uint64())
}

// scanMemoryLimiter keeps the runtime's soft memory limit tracking the live heap
// while at least one scan is running.
//
// GOGC=100 lets the heap reach twice the live set before collecting, so the peak
// is a *multiple* of the result. The multiple does not change with volume size
// but the absolute cost does — a 10M-node tree would peak near 1.8 GB. Tracking
// the live set turns that headroom into a constant instead (ADR-0058 §1).
//
// A fixed GOMEMLIMIT was measured and rejected: it only helps while it stays
// above the live set, and the live set is about 92 bytes per node, so any
// constant tuned here lands below the live set on a large enough volume and
// becomes pure slowdown (R-059 §4.3).
type scanMemoryLimiter struct {
	mu      sync.Mutex
	holders int
	stop    chan struct{}
	done    chan struct{}
}

// hold starts tracking and returns the release. The release is reference
// counted: a concurrent scan finishing must not lift the limit for one that is
// still running. Calling it more than once is a no-op.
func (l *scanMemoryLimiter) hold() func() {
	l.mu.Lock()
	l.holders++
	first := l.holders == 1
	if first {
		l.stop = make(chan struct{})
		l.done = make(chan struct{})
		go l.track(l.stop, l.done)
	}
	l.mu.Unlock()
	if first {
		// Applied here rather than waiting for the first tick, so a scan shorter
		// than the interval still runs under a limit — and so a test can tell a
		// held scan from an unheld one.
		debug.SetMemoryLimit(liveHeapBytes() + scanMemoryHeadroom)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.holders--
			last := l.holders == 0
			stop, done := l.stop, l.done
			l.mu.Unlock()
			if !last {
				return
			}
			close(stop)
			<-done
			// Restored only after the tracker has stopped, so a late write from
			// it cannot leave the process running under a scan's limit. This knob
			// is process-global.
			debug.SetMemoryLimit(noMemoryLimit)
		})
	}
}

func (l *scanMemoryLimiter) track(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(scanMemoryLimitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			debug.SetMemoryLimit(liveHeapBytes() + scanMemoryHeadroom)
		}
	}
}
