package testing

import (
	"github.com/dat267/gpi/server"
)

// TestServerOptions configure CreateTestServer.
type TestServerOptions struct {
	Host               server.ServerHost
	ServerID           string
	Listeners          []server.ServerListener
	MaxFrameLength     *int64
	HandshakeTimeoutMS *int
	OnError            func(err error)
}

// TestServer is an unstarted server plus its host.
type TestServer struct {
	Server *server.Server
	Host   server.ServerHost
}

// DefaultTestServerID matches upstream's deterministic conformance server id.
const DefaultTestServerID = "00000000-0000-4000-8000-000000000001"

// CreateTestServer builds an unstarted Server with deterministic defaults for
// transport conformance tests.
func CreateTestServer(options TestServerOptions) (*TestServer, error) {
	host := options.Host
	if host == nil {
		host = NewTestServerHost()
	}
	serverID := options.ServerID
	if serverID == "" {
		serverID = DefaultTestServerID
	}
	created, err := server.NewServer(host, server.ServerOptions{
		Listeners:          options.Listeners,
		ServerID:           serverID,
		MaxFrameLength:     options.MaxFrameLength,
		HandshakeTimeoutMS: options.HandshakeTimeoutMS,
		OnError:            options.OnError,
	})
	if err != nil {
		return nil, err
	}
	return &TestServer{Server: created, Host: host}, nil
}
