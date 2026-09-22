package throttle

import (
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Limit is one client's effective rate caps in kilobits per second. A zero
// side means unlimited in that direction.
type Limit struct {
	DownKbps int
	UpKbps   int
}

// Manager holds the per-client rate-limiting state and owns the relay's
// lifecycle. The relay binds lazily: an install that never throttles a client
// never occupies the loopback ports.
type Manager struct {
	mu     sync.Mutex
	limits map[string]Limit
	down   map[string]*bucket
	up     map[string]*bucket
	relay  *relay
}

// Default is the process-wide manager; the panel has exactly one relay set.
var Default = NewManager()

func NewManager() *Manager {
	return &Manager{
		limits: map[string]Limit{},
		down:   map[string]*bucket{},
		up:     map[string]*bucket{},
	}
}

// SetLimits replaces the throttled set. Emails no longer present have their
// buckets zeroed so in-flight relay connections immediately run unlimited;
// new connections stop being routed through the relay once the service layer
// drops their Xray rules.
func (m *Manager) SetLimits(limits map[string]Limit) {
	m.mu.Lock()
	for email, b := range m.down {
		if _, ok := limits[email]; !ok {
			b.setRate(0)
		}
	}
	for email, b := range m.up {
		if _, ok := limits[email]; !ok {
			b.setRate(0)
		}
	}
	for email, lim := range limits {
		downBps := KbpsToBytesPerSec(lim.DownKbps)
		upBps := KbpsToBytesPerSec(lim.UpKbps)
		if b := m.down[email]; b != nil {
			b.setRate(downBps)
		} else if downBps > 0 {
			m.down[email] = newBucket(downBps)
		}
		if b := m.up[email]; b != nil {
			b.setRate(upBps)
		} else if upBps > 0 {
			m.up[email] = newBucket(upBps)
		}
	}
	m.limits = limits
	var startErr error
	if len(limits) > 0 {
		if m.relay == nil {
			m.relay = newRelay(EgressAddr, m)
		}
		startErr = m.relay.start()
	} else if m.relay != nil {
		m.relay.stop()
		m.relay = nil
	}
	m.mu.Unlock()
	if startErr != nil {
		// Retried on every SetLimits tick; the service layer also checks
		// RelayHealthy() before injecting the Xray side.
		logger.Warning("throttle: relay start failed:", startErr)
	}
}

// RelayHealthy reports whether the relay listener is bound whenever any client
// is throttled. The service layer must not inject throttle rules while this is
// false, or Xray would send user traffic to whatever holds the port.
func (m *Manager) RelayHealthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.limits) == 0 {
		return true
	}
	return m.relay != nil && m.relay.listening()
}

func (m *Manager) bucketsFor(email string) (*bucket, *bucket) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.down[email], m.up[email]
}
