package lifecycle

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

const gatewayCacheTTL = 5 * time.Second
const gatewayCacheLimit = 256

type gatewayKey struct{ profile, workspace string }
type gatewayFlightKey struct {
	gatewayKey
	authorization string
}
type gatewayEntry struct {
	access  Access
	expires time.Time
}
type gatewayFlight struct {
	done        chan struct{}
	access      Access
	status      int
	invalidated bool
}
type gatewayCache struct {
	mu      sync.Mutex
	entries map[gatewayKey]gatewayEntry
	flights map[gatewayFlightKey]*gatewayFlight
}

// Invalidating in-flight observations also prevents a pre-transition read being
// inserted after a state write. Never hold this mutex across Kubernetes calls:
// Access may itself save a newly observed worker incarnation.
func (c *Controller) invalidateGateway(profile, workspace string) {
	cache := &c.gatewayCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	key := gatewayKey{profile, workspace}
	delete(cache.entries, key)
	for k, flight := range cache.flights {
		if k.gatewayKey == key {
			flight.invalidated = true
		}
	}
}

func bearerMatches(authorization, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(authorization), []byte("Bearer "+token)) == 1
}

func (c *Controller) gatewayAccess(ctx context.Context, profile, workspace, authorization string) (Access, int) {
	for attempt := 0; attempt < 2; attempt++ {
		access, status := c.gatewayAccessOnce(ctx, profile, workspace, authorization)
		if status != 0 {
			return access, status
		}
	}
	return Access{}, http.StatusServiceUnavailable
}

func (c *Controller) gatewayAccessOnce(ctx context.Context, profile, workspace, authorization string) (Access, int) {
	cache := &c.gatewayCache
	key := gatewayKey{profile, workspace}
	flightKey := gatewayFlightKey{key, authorization}
	cache.mu.Lock()
	now := time.Now()
	for k, entry := range cache.entries {
		if !now.Before(entry.expires) {
			delete(cache.entries, k)
		}
	}
	if entry, ok := cache.entries[key]; ok {
		cache.mu.Unlock()
		if !bearerMatches(authorization, entry.access.Token) {
			return Access{}, http.StatusUnauthorized
		}
		return entry.access, http.StatusOK
	}
	if flight, ok := cache.flights[flightKey]; ok {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return Access{}, http.StatusServiceUnavailable
		case <-flight.done:
			return flight.access, flight.status
		}
	}
	if len(cache.flights) >= gatewayCacheLimit {
		cache.mu.Unlock()
		return Access{}, http.StatusServiceUnavailable
	}
	if cache.flights == nil {
		cache.flights = make(map[gatewayFlightKey]*gatewayFlight)
	}
	flight := &gatewayFlight{done: make(chan struct{})}
	cache.flights[flightKey] = flight
	cache.mu.Unlock()
	access, status := c.readGatewayAccess(ctx, profile, workspace, authorization)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.flights, flightKey)
	if !flight.invalidated {
		flight.access, flight.status = access, status
		if status == http.StatusOK {
			if cache.entries == nil {
				cache.entries = make(map[gatewayKey]gatewayEntry)
			}
			if len(cache.entries) >= gatewayCacheLimit {
				for k := range cache.entries {
					delete(cache.entries, k)
					break
				}
			}
			cache.entries[key] = gatewayEntry{access: access, expires: time.Now().Add(gatewayCacheTTL)}
		}
	}
	close(flight.done)
	return flight.access, flight.status
}

func (c *Controller) readGatewayAccess(ctx context.Context, profile, workspace, authorization string) (Access, int) {
	state, _, err := c.load(ctx, profile, workspace)
	if err != nil || state.Phase != "running" || state.Handle == nil || !bearerMatches(authorization, c.token(workspace, state.Epoch)) {
		return Access{}, http.StatusUnauthorized
	}
	access, err := c.Access(ctx, profile, workspace)
	if err != nil || !bearerMatches(authorization, access.Token) {
		return Access{}, http.StatusServiceUnavailable
	}
	return access, http.StatusOK
}
