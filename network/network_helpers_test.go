package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"

	gerr "github.com/gatewayd-io/gatewayd/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type WriteBuffer struct {
	Bytes []byte

	msgStart int
}

// MockProxy implements the IProxy interface for testing purposes.
type MockProxy struct {
	name string
}

// writeStartupMsg writes a PostgreSQL startup message to the buffer.
func writeStartupMsg(buf *WriteBuffer, user, database, appName string) {
	// Write startup message header
	buf.msgStart = len(buf.Bytes)
	buf.Bytes = append(buf.Bytes, 0, 0, 0, 0)

	// Write protocol version
	buf.Bytes = append(buf.Bytes, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(buf.Bytes[len(buf.Bytes)-4:], uint32(196608))

	buf.WriteString("user")
	buf.WriteString(user)
	buf.WriteString("database")
	buf.WriteString(database)
	buf.WriteString("application_name")
	buf.WriteString(appName)
	buf.WriteString("")

	// Write message length
	binary.BigEndian.PutUint32(
		buf.Bytes[buf.msgStart:], uint32(len(buf.Bytes)-buf.msgStart))
}

// WriteString writes a null-terminated string to the buffer.
func (buf *WriteBuffer) WriteString(s string) {
	buf.Bytes = append(buf.Bytes, s...)
	buf.Bytes = append(buf.Bytes, 0)
}

// CreatePostgreSQLPacket creates a PostgreSQL packet.
func CreatePostgreSQLPacket(typ byte, msg []byte) []byte {
	packet := make([]byte, 1+4+len(msg))

	packet[0] = typ
	binary.BigEndian.PutUint32(packet[1:], uint32(len(msg)+4))
	for i, b := range msg {
		packet[i+5] = b
	}

	return packet
}

// CreatePgStartupPacket creates a PostgreSQL startup packet.
func CreatePgStartupPacket() []byte {
	buf := &WriteBuffer{}
	writeStartupMsg(buf, "postgres", "postgres", "gatewayd")
	return buf.Bytes
}

// CreatePgTerminatePacket creates a PostgreSQL terminate packet.
func CreatePgTerminatePacket() []byte {
	return []byte{'X', 0, 0, 0, 4}
}

func CollectAndComparePrometheusMetrics(t *testing.T) {
	t.Helper()

	const metadata = `
		# HELP gatewayd_bytes_received_from_client Number of bytes received from client
		# TYPE gatewayd_bytes_received_from_client summary
		# HELP gatewayd_bytes_received_from_server Number of bytes received from server
		# TYPE gatewayd_bytes_received_from_server summary
		# HELP gatewayd_bytes_sent_to_client Number of bytes sent to client
		# TYPE gatewayd_bytes_sent_to_client summary
		# HELP gatewayd_bytes_sent_to_server Number of bytes sent to server
		# TYPE gatewayd_bytes_sent_to_server summary
		# HELP gatewayd_client_connections Number of client connections
		# TYPE gatewayd_client_connections gauge
		# HELP gatewayd_plugin_hooks_executed_total Number of plugin hooks executed
		# TYPE gatewayd_plugin_hooks_executed_total counter
		# HELP gatewayd_plugin_hooks_registered_total Number of plugin hooks registered
		# TYPE gatewayd_plugin_hooks_registered_total counter
		# HELP gatewayd_plugins_loaded_total Number of plugins loaded
		# TYPE gatewayd_plugins_loaded_total counter
		# HELP gatewayd_proxied_connections Number of proxy connects
		# TYPE gatewayd_proxied_connections gauge
		# HELP gatewayd_proxy_health_checks_total Number of proxy health checks
		# TYPE gatewayd_proxy_health_checks_total counter
		# HELP gatewayd_proxy_passthrough_terminations_total Number of proxy passthrough terminations by plugins
		# TYPE gatewayd_proxy_passthrough_terminations_total counter
		# HELP gatewayd_proxy_passthroughs_to_client_total Number of successful proxy passthroughs
		# TYPE gatewayd_proxy_passthroughs_to_client_total counter
		# HELP gatewayd_proxy_passthroughs_to_server_total Number of successful proxy passthroughs
		# TYPE gatewayd_proxy_passthroughs_to_server_total counter
		# HELP gatewayd_server_connections Number of server connections
		# TYPE gatewayd_server_connections gauge
		# HELP gatewayd_server_ticks_fired_total Total number of server ticks fired
		# TYPE gatewayd_server_ticks_fired_total counter
		# HELP gatewayd_traffic_bytes Number of total bytes passed through GatewayD via client or server
		# TYPE gatewayd_traffic_bytes summary
		`

	var (
		want = metadata + `
			gatewayd_bytes_received_from_client_sum 72
			gatewayd_bytes_received_from_client_count 3
			gatewayd_bytes_received_from_server_sum 24
			gatewayd_bytes_received_from_server_count 2
			gatewayd_bytes_sent_to_client_sum 24
			gatewayd_bytes_sent_to_client_count 1
			gatewayd_bytes_sent_to_server_sum 72
			gatewayd_bytes_sent_to_server_count 2
			gatewayd_client_connections 0
			gatewayd_plugin_hooks_executed_total 17
			gatewayd_plugin_hooks_registered_total 0
			gatewayd_plugins_loaded_total 0
			gatewayd_proxied_connections 0
			gatewayd_proxy_health_checks_total 0
			gatewayd_proxy_passthrough_terminations_total 0
			gatewayd_proxy_passthroughs_to_client_total 1
			gatewayd_proxy_passthroughs_to_server_total 1
			gatewayd_server_connections 1
			gatewayd_traffic_bytes_sum 192
			gatewayd_traffic_bytes_count 8
			gatewayd_server_ticks_fired_total 1
		`

		metrics = []string{
			"gatewayd_bytes_received_from_client",
			"gatewayd_bytes_received_from_server",
			"gatewayd_bytes_sent_to_client",
			"gatewayd_bytes_sent_to_server",
			"gatewayd_client_connections",
			"gatewayd_plugin_hooks_executed_total",
			"gatewayd_plugin_hooks_registered_total",
			"gatewayd_plugins_loaded_total",
			"gatewayd_proxied_connections",
			"gatewayd_proxy_health_checks_total",
			"gatewayd_proxy_passthrough_terminations_total",
			"gatewayd_proxy_passthroughs_total",
			"gatewayd_server_connections",
			"gatewayd_traffic_bytes",
			"gatewayd_server_ticks_fired_total",
		}
	)
	require.NoError(t,
		testutil.GatherAndCompare(prometheus.DefaultGatherer, strings.NewReader(want), metrics...))
}

// Connect is a mock implementation of the Connect method in the IProxy interface.
func (m MockProxy) Connect(_ *ConnWrapper) *gerr.GatewayDError {
	return nil
}

// Disconnect is a mock implementation of the Disconnect method in the IProxy interface.
func (m MockProxy) Disconnect(_ *ConnWrapper) *gerr.GatewayDError {
	return nil
}

// PassThroughToServer is a mock implementation of the PassThroughToServer method in the IProxy interface.
func (m MockProxy) PassThroughToServer(_ *ConnWrapper, _ *Stack) *gerr.GatewayDError {
	return nil
}

// PassThroughToClient is a mock implementation of the PassThroughToClient method in the IProxy interface.
func (m MockProxy) PassThroughToClient(_ *ConnWrapper, _ *Stack) *gerr.GatewayDError {
	return nil
}

// IsHealthy is a mock implementation of the IsHealthy method in the IProxy interface.
func (m MockProxy) IsHealthy(_ *Client) (*Client, *gerr.GatewayDError) {
	return nil, nil
}

// IsExhausted is a mock implementation of the IsExhausted method in the IProxy interface.
func (m MockProxy) IsExhausted() bool {
	return false
}

// Shutdown is a mock implementation of the Shutdown method in the IProxy interface.
func (m MockProxy) Shutdown() {}

// AvailableConnectionsString is a mock implementation of the AvailableConnectionsString method in the IProxy interface.
func (m MockProxy) AvailableConnectionsString() []string {
	return nil
}

// BusyConnectionsString is a mock implementation of the BusyConnectionsString method in the IProxy interface.
func (m MockProxy) BusyConnectionsString() []string {
	return nil
}

// GetName returns the name of the MockProxy.
func (m MockProxy) GetName() string {
	return m.name
}

// Mock implementation of IConnWrapper.
type MockConnWrapper struct {
	mock.Mock
}

func (m *MockConnWrapper) Conn() net.Conn {
	args := m.Called()
	conn, ok := args.Get(0).(net.Conn)
	if !ok {
		panic(fmt.Sprintf("expected net.Conn but got %T", args.Get(0)))
	}
	return conn
}

func (m *MockConnWrapper) UpgradeToTLS(upgrader UpgraderFunc) *gerr.GatewayDError {
	args := m.Called(upgrader)
	err, ok := args.Get(0).(*gerr.GatewayDError)
	if !ok {
		panic(fmt.Sprintf("expected *gerr.GatewayDError but got %T", args.Get(0)))
	}
	return err
}

func (m *MockConnWrapper) Close() error {
	args := m.Called()
	if err := args.Error(0); err != nil {
		return fmt.Errorf("failed to close connection: %w", err)
	}
	return nil
}

func (m *MockConnWrapper) Write(data []byte) (int, error) {
	args := m.Called(data)
	return args.Int(0), args.Error(1)
}

func (m *MockConnWrapper) Read(data []byte) (int, error) {
	args := m.Called(data)
	return args.Int(0), args.Error(1)
}

func (m *MockConnWrapper) RemoteAddr() net.Addr {
	args := m.Called()
	addr, ok := args.Get(0).(net.Addr)
	if !ok {
		panic(fmt.Sprintf("expected net.Addr but got %T", args.Get(0)))
	}
	return addr
}

func (m *MockConnWrapper) LocalAddr() net.Addr {
	args := m.Called()
	addr, ok := args.Get(0).(net.Addr)
	if !ok {
		panic(fmt.Sprintf("expected net.Addr but got %T", args.Get(0)))
	}
	return addr
}

func (m *MockConnWrapper) IsTLSEnabled() bool {
	args := m.Called()
	return args.Bool(0)
}
