// Package facets is a Go port of @earendil-works/chord's facet runtime
// (pi/packages/chord/src/facets and the facet surface of src/api.ts).
//
// Ground truth: pi/packages/chord/src at the pinned upstream commit.
//
// D20 (layout): upstream declares the facet types in the package root
// (types.ts) and the runtime in facets/host.ts. In Go the facet API needs
// chord/services (declared members carry op-carrying replicated states), and
// chord/services already imports the chord root, so the facet API lives here
// instead of the root. The re-exported api.ts helpers live next to the types
// they construct.
package facets

import (
	"context"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/services"
)

// RemoteServices is a connected set of remote service handles (upstream
// RemoteServices).
type RemoteServices interface {
	// Use acquires the singleton facade for one service.
	Use(service chord.Service) (*services.ServiceFacade, error)
	// Observe registers a handler per live keyed instance.
	Observe(service chord.Service, handler func(service *services.GuardedFacade, ctx context.Context) error) (func(), error)
	// Ready waits until every acquired service installed its initial snapshot.
	Ready(ctx context.Context) error
	// Dispose releases every acquired service.
	Dispose(ctx context.Context) error
}

// ServiceSource is one pluggable remote service source (upstream
// RemoteServiceSource).
type ServiceSource interface {
	// AcceptsUnavailableServices reports whether this source may provisionally
	// own absent requirements.
	AcceptsUnavailableServices() bool
	// Catalogue lists the services this source offers.
	Catalogue(ctx context.Context) ([]chord.ServiceCatalogueEntry, error)
	// Open connects a binding for the requested service ids.
	Open(options ServiceSourceOpenOptions) (RemoteServices, error)
}

// ServiceSourceOpenOptions configure one opened source.
type ServiceSourceOpenOptions struct {
	Services     []chord.Service
	AssertAccess func() error
	OnError      func(error)
}

// ServiceSpawner stages keyed service instances declared by a facet (upstream
// ServiceSpawner<T>).
type ServiceSpawner interface {
	// Spawn creates one instance while the facet is active.
	Spawn(key string, implementation any, members services.Members) (func(), error)
}

// FacetEnvironment is the setup-time environment handed to one facet (upstream
// FacetEnvironment).
type FacetEnvironment interface {
	// Use declares a hard dependency on one singleton service and returns its
	// stable handle.
	Use(service chord.Service) (*services.SlotView, error)
	// Observe declares a hard dependency on a keyed service and observes each
	// live instance.
	Observe(service chord.Service, handler func(service *services.SlotView, ctx context.Context) error) error
	// Provide declares and installs this facet's singleton implementation.
	Provide(service chord.Service, implementation any, members services.Members) error
	// ProvideMany declares ownership of a keyed service and returns its
	// deferred spawning capability.
	ProvideMany(service chord.Service) (ServiceSpawner, error)
	// ReplicatedState creates initialized mutable state suitable for exposing
	// through a service implementation.
	ReplicatedState(initial chord.JsonValue) (*services.MutableState, error)
	// Own gives the facet ownership of a resource cleanup function.
	Own(disposal func() error) error
	// OnActivate registers asynchronous initialization after dependencies are
	// bound and ready.
	OnActivate(callback func() error) error
	// OnDeactivate registers final facet teardown.
	OnDeactivate(callback func() error) error
}

// Facet is one composable unit of application wiring (upstream Facet).
type Facet struct {
	ID string
	// Setup declares dependencies and provisions. It must be synchronous.
	Setup func(env FacetEnvironment) error
}

// FacetOptions configure a facet host.
type FacetOptions struct {
	Facets         []Facet
	ServiceSources []ServiceSource
	OnError        func(error)
}

// FacetHost is an active host for one complete set of facets (upstream
// FacetHost).
type FacetHost struct {
	Services *services.RemoteServiceProvider
	Reload   func(facets []Facet) error
	Dispose  func() error
}

// LoadedFacets is one loader's result (upstream LoadedFacets).
type LoadedFacets struct {
	Facets  []Facet
	Dispose func() error
}

// FacetLoader loads a facet set (upstream FacetLoader).
type FacetLoader interface {
	Load() (LoadedFacets, error)
}
