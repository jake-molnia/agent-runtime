package lifecycle

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const gatewayCacheTTL = 5 * time.Second
const gatewayCacheLimit = 256

type gatewayKey struct{ profile, workspace string }
type gatewayFlightKey struct {
	gatewayKey
	authorization string
	control       bool
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
	return c.cachedAccess(ctx, profile, workspace, authorization, false)
}

// Only the pool-authenticated control HTTP handler calls this bounded read path.
// Mutation and strong-observation callers continue to use Access directly.
func (c *Controller) controlAccess(ctx context.Context, profile, workspace string) (Access, int) {
	return c.cachedAccess(ctx, profile, workspace, "", true)
}

func (c *Controller) cachedAccess(ctx context.Context, profile, workspace, authorization string, control bool) (Access, int) {
	for attempt := 0; attempt < 2; attempt++ {
		access, status := c.cachedAccessOnce(ctx, profile, workspace, authorization, control)
		if status == 0 {
			continue
		}
		if status != http.StatusOK {
			return access, status
		}
		// Worker probes can be cached; authorization must observe shared lifecycle
		// state even when another replica owns the transition or the in-flight read.
		current, _, err := c.load(ctx, profile, workspace)
		if err != nil {
			c.invalidateGateway(profile, workspace)
			return Access{}, http.StatusServiceUnavailable
		}
		if digest(current) != digest(access.State) {
			c.invalidateGateway(profile, workspace)
			continue
		}
		if !control && (current.Request.Action != EnsureRunning || !bearerMatches(authorization, access.Token)) {
			return Access{}, http.StatusUnauthorized
		}
		return access, http.StatusOK
	}
	return Access{}, http.StatusServiceUnavailable
}

func (c *Controller) cachedAccessOnce(ctx context.Context, profile, workspace, authorization string, control bool) (Access, int) {
	cache := &c.gatewayCache
	key := gatewayKey{profile, workspace}
	flightKey := gatewayFlightKey{key, authorization, control}
	cache.mu.Lock()
	now := time.Now()
	for k, entry := range cache.entries {
		if !now.Before(entry.expires) {
			delete(cache.entries, k)
		}
	}
	if entry, ok := cache.entries[key]; ok {
		cache.mu.Unlock()
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
	var access Access
	var status int
	if control {
		var err error
		access, err = c.Access(ctx, profile, workspace)
		status = http.StatusOK
		if err != nil {
			status = http.StatusServiceUnavailable
			if apierrors.IsNotFound(err) {
				status = http.StatusNotFound
			}
		}
	} else {
		access, status = c.readGatewayAccess(ctx, profile, workspace, authorization)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.flights, flightKey)
	if !flight.invalidated {
		flight.access, flight.status = access, status
		if status == http.StatusOK && access.State.Phase == "running" && access.Token != "" {
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
	if err != nil || state.Request.Action != EnsureRunning || state.Phase != "running" || state.Handle == nil || !bearerMatches(authorization, c.token(workspace, state.Epoch)) {
		return Access{}, http.StatusUnauthorized
	}
	access, err := c.Access(ctx, profile, workspace)
	if err != nil || !bearerMatches(authorization, access.Token) {
		return Access{}, http.StatusServiceUnavailable
	}
	return access, http.StatusOK
}
