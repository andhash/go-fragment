package fragment

import (
	"sort"
	"sync"
	"time"
)

// Histogram names a caller can export. This module does not import Prometheus.
const (
	MetricOpTotal        = "fragment_op_total"
	MetricPrepareSeconds = "fragment_prepare_seconds"
	MetricPayUntilSend   = "fragment_pay_until_send_seconds"
	MetricConfirmSeconds = "fragment_confirm_seconds"
)

// Event is one finished operation. IdempotencyKey is the caller key, not a storefront id.
type Event struct {
	Op             string
	Product        Product
	Result         string
	Duration       time.Duration
	IdempotencyKey string
	ReqID          string
}

// Metrics receives operation events. The default implementation discards them.
// New does not start a goroutine.
type Metrics interface {
	Observe(e Event)
}

type discardMetrics struct{}

func (discardMetrics) Observe(Event) {}

// MemoryMetrics counts results and keeps durations for prepare and for pay until Send.
type MemoryMetrics struct {
	mu      sync.Mutex
	counts  map[string]int
	prepare []time.Duration
	pay     []time.Duration
	confirm []time.Duration
}

func NewMemoryMetrics() *MemoryMetrics {
	return &MemoryMetrics{counts: map[string]int{}}
}

func (m *MemoryMetrics) Observe(e Event) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = map[string]int{}
	}
	m.counts[e.Op+"|"+e.Result]++
	switch e.Op {
	case "prepare":
		m.prepare = append(m.prepare, e.Duration)
	case "pay":
		m.pay = append(m.pay, e.Duration)
	case "confirm":
		m.confirm = append(m.confirm, e.Duration)
	}
}

// Count returns how many events were observed for op and result.
func (m *MemoryMetrics) Count(op, result string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[op+"|"+result]
}

// P95 returns the 95th percentile duration for prepare, pay, or confirm.
func (m *MemoryMetrics) P95(op string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var xs []time.Duration
	switch op {
	case "prepare":
		xs = m.prepare
	case "pay":
		xs = m.pay
	case "confirm":
		xs = m.confirm
	}
	if len(xs) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), xs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := (len(cp)*95+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}
