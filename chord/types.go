package chord

// Port of the service portion of packages/chord/src/types.ts.

// ServiceMode is the replication mode of a service.
type ServiceMode = string

const (
	ServiceModeSingleton ServiceMode = "singleton"
	ServiceModeKeyed     ServiceMode = "keyed"
)

// ServiceInstanceAddress identifies one service instance.
type ServiceInstanceAddress struct {
	Key        string `json:"key"`
	Generation int    `json:"generation"`
}

// ServiceCall routes one RPC call.
type ServiceCall struct {
	ServiceID string                  `json:"serviceId"`
	Member    string                  `json:"member"`
	Instance  *ServiceInstanceAddress `json:"instance,omitempty"`
	Args      []JsonValue             `json:"args"`
}

// ServiceCatalogueEntry is one catalogue row.
type ServiceCatalogueEntry struct {
	ServiceID string      `json:"serviceId"`
	Mode      ServiceMode `json:"mode"`
}

// Context is the Chord context handle (upstream Context); the full context
// implementation is queued, so this aliases the value shape used by services.
type Context = any
