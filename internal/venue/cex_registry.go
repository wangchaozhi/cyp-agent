package venue

import (
	"sort"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// VenueRegistry preserves deterministic registration order while allowing an
// adapter to be replaced by ID. It is safe for concurrent API reads.
type VenueRegistry struct {
	mu     sync.RWMutex
	venues map[string]Venue
	order  []string
}

func NewVenueRegistry(venues ...Venue) *VenueRegistry {
	registry := &VenueRegistry{venues: make(map[string]Venue)}
	for _, current := range venues {
		registry.Register(current)
	}
	return registry
}

func (registry *VenueRegistry) Register(current Venue) {
	if registry == nil || current == nil || current.ID() == "" {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.venues == nil {
		registry.venues = make(map[string]Venue)
	}
	if _, exists := registry.venues[current.ID()]; !exists {
		registry.order = append(registry.order, current.ID())
	}
	registry.venues[current.ID()] = current
}

func (registry *VenueRegistry) Get(id string) (Venue, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	current, ok := registry.venues[id]
	return current, ok
}

func (registry *VenueRegistry) All() []Venue {
	if registry == nil {
		return []Venue{}
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]Venue, 0, len(registry.order))
	for _, id := range registry.order {
		if current := registry.venues[id]; current != nil {
			result = append(result, current)
		}
	}
	return result
}

func (registry *VenueRegistry) Describe() []contracts.VenueInfo {
	all := registry.All()
	result := make([]contracts.VenueInfo, 0, len(all))
	for _, current := range all {
		caps := current.Caps()
		result = append(result, contracts.VenueInfo{
			ID: current.ID(), Kind: string(current.Kind()), Configured: current.IsConfigured(),
			Spot: caps.Spot, Perp: caps.Perp,
			NativeProtectiveOrders: caps.NativeProtectiveOrders, ReadOnly: caps.ReadOnly,
		})
	}
	return result
}

// IDs returns a sorted snapshot useful for validation/error messages without
// exposing registry internals.
func (registry *VenueRegistry) IDs() []string {
	all := registry.All()
	ids := make([]string, 0, len(all))
	for _, current := range all {
		ids = append(ids, current.ID())
	}
	sort.Strings(ids)
	return ids
}
