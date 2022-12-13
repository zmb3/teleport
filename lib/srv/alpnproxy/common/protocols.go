/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

import (
	"strings"

	"github.com/gravitational/trace"
	"golang.org/x/exp/slices"

	"github.com/zmb3/teleport/lib/defaults"
)

// Protocol is the TLS ALPN protocol type.
type Protocol string

const (
	// ProtocolPostgres is TLS ALPN protocol value used to indicate Postgres protocol.
	ProtocolPostgres Protocol = "teleport-postgres"

	// ProtocolMySQL is TLS ALPN protocol value used to indicate MySQL protocol.
	ProtocolMySQL Protocol = "teleport-mysql"

	// ProtocolMongoDB is TLS ALPN protocol value used to indicate Mongo protocol.
	ProtocolMongoDB Protocol = "teleport-mongodb"

	// ProtocolRedisDB is TLS ALPN protocol value used to indicate Redis protocol.
	ProtocolRedisDB Protocol = "teleport-redis"

	// ProtocolSQLServer is the TLS ALPN protocol value used to indicate SQL Server protocol.
	ProtocolSQLServer Protocol = "teleport-sqlserver"

	// ProtocolSnowflake is TLS ALPN protocol value used to indicate Snowflake protocol.
	ProtocolSnowflake Protocol = "teleport-snowflake"

	// ProtocolCassandra is the TLS ALPN protocol value used to indicate Cassandra protocol.
	ProtocolCassandra Protocol = "teleport-cassandra"

	// ProtocolElasticsearch is TLS ALPN protocol value used to indicate Elasticsearch protocol.
	ProtocolElasticsearch Protocol = "teleport-elasticsearch"

	// ProtocolProxySSH is TLS ALPN protocol value used to indicate Proxy SSH protocol.
	ProtocolProxySSH Protocol = "teleport-proxy-ssh"

	// ProtocolReverseTunnel is TLS ALPN protocol value used to indicate Proxy reversetunnel protocol.
	ProtocolReverseTunnel Protocol = "teleport-reversetunnel"

	// ProtocolReverseTunnelV2 is TLS ALPN protocol value used to indicate reversetunnel clients
	// that are aware of proxy peering. This is only used on the client side to allow intermediate
	// load balancers to make decisions based on the ALPN header. ProtocolReverseTunnel should still
	// be included in the list of ALPN header for the proxy server to handle the connection properly.
	ProtocolReverseTunnelV2 Protocol = "teleport-reversetunnelv2"

	// ProtocolHTTP is TLS ALPN protocol value used to indicate HTTP 1.1 protocol
	ProtocolHTTP Protocol = "http/1.1"

	// ProtocolHTTP2 is TLS ALPN protocol value used to indicate HTTP2 protocol.
	ProtocolHTTP2 Protocol = "h2"

	// ProtocolDefault is default TLS ALPN value.
	ProtocolDefault Protocol = ""

	// ProtocolAuth allows dialing local/remote auth service based on SNI cluster name value.
	ProtocolAuth Protocol = "teleport-auth@"

	// ProtocolProxyGRPC is TLS ALPN protocol value used to indicate gRPC
	// traffic intended for the Teleport proxy.
	ProtocolProxyGRPC Protocol = "teleport-proxy-grpc"

	// ProtocolMySQLWithVerPrefix is TLS ALPN prefix used by tsh to carry
	// MySQL server version.
	ProtocolMySQLWithVerPrefix = Protocol(string(ProtocolMySQL) + "-")

	// ProtocolTCP is TLS ALPN protocol value used to indicate plain TCP connection.
	ProtocolTCP Protocol = "teleport-tcp"

	// ProtocolPingSuffix is TLS ALPN suffix used to wrap connections with
	// Ping.
	ProtocolPingSuffix Protocol = "-ping"
)

// SupportedProtocols is the list of supported ALPN protocols.
var SupportedProtocols = append(
	ProtocolsWithPing(ProtocolsWithPingSupport...),
	append([]Protocol{
		// HTTP needs to be prioritized over HTTP2 due to a bug in Chrome:
		// https://bugs.chromium.org/p/chromium/issues/detail?id=1379017
		// If Chrome resolves this, we can switch the prioritization. We may
		// also be able to get around this if https://github.com/golang/go/issues/49918
		// is implemented and we can enable HTTP2 websockets on our end, but
		// it's less clear this will actually fix the issue.
		ProtocolHTTP,
		ProtocolHTTP2,
		ProtocolProxySSH,
		ProtocolReverseTunnel,
		ProtocolAuth,
		ProtocolTCP,
	}, DatabaseProtocols...)...,
)

// ProtocolsToString converts the list of Protocols to the list of strings.
func ProtocolsToString(protocols []Protocol) []string {
	out := make([]string, 0, len(protocols))
	for _, v := range protocols {
		out = append(out, string(v))
	}
	return out
}

// ToALPNProtocol maps provided database protocol to ALPN protocol.
func ToALPNProtocol(dbProtocol string) (Protocol, error) {
	switch dbProtocol {
	case defaults.ProtocolMySQL:
		return ProtocolMySQL, nil
	case defaults.ProtocolPostgres, defaults.ProtocolCockroachDB:
		return ProtocolPostgres, nil
	case defaults.ProtocolMongoDB:
		return ProtocolMongoDB, nil
	case defaults.ProtocolRedis:
		return ProtocolRedisDB, nil
	case defaults.ProtocolSQLServer:
		return ProtocolSQLServer, nil
	case defaults.ProtocolSnowflake:
		return ProtocolSnowflake, nil
	case defaults.ProtocolCassandra:
		return ProtocolCassandra, nil
	case defaults.ProtocolElasticsearch:
		return ProtocolElasticsearch, nil
	default:
		return "", trace.NotImplemented("%q protocol is not supported", dbProtocol)
	}
}

// IsDBTLSProtocol returns if DB protocol has supported native TLS protocol.
// where connection can be TLS terminated on ALPN proxy side.
// For protocol like MySQL or Postgres where custom TLS implementation is used the incoming
// connection needs to be forwarded to proxy database service where custom TLS handler is invoked
// to terminated DB connection.
func IsDBTLSProtocol(protocol Protocol) bool {
	dbTLSProtocols := []Protocol{
		ProtocolMongoDB,
		ProtocolRedisDB,
		ProtocolSQLServer,
		ProtocolSnowflake,
		ProtocolCassandra,
		ProtocolElasticsearch,
	}

	return slices.Contains(
		append(dbTLSProtocols, ProtocolsWithPing(dbTLSProtocols...)...),
		protocol,
	)
}

// DatabaseProtocols is the list of the database protocols supported.
var DatabaseProtocols = []Protocol{
	ProtocolPostgres,
	ProtocolMySQL,
	ProtocolMongoDB,
	ProtocolRedisDB,
	ProtocolSQLServer,
	ProtocolSnowflake,
	ProtocolCassandra,
	ProtocolElasticsearch,
}

// ProtocolsWithPingSupport is the list of protocols that Ping connection is
// supported. For now, only database protocols are supported.
var ProtocolsWithPingSupport = DatabaseProtocols

// ProtocolsWithPing receives a list a protocols and returns a list of them with
// the Ping protocol suffix.
func ProtocolsWithPing(protocols ...Protocol) []Protocol {
	res := make([]Protocol, len(protocols))
	for i := range res {
		res[i] = ProtocolWithPing(protocols[i])
	}

	return res
}

// ProtocolWithPing receives a protocol and returns it with the Ping protocol
// suffix.
func ProtocolWithPing(protocol Protocol) Protocol {
	return Protocol(string(protocol) + string(ProtocolPingSuffix))
}

// IsPingProtocol checks if the provided protocol is suffixed with Ping.
func IsPingProtocol(protocol Protocol) bool {
	return strings.HasSuffix(string(protocol), string(ProtocolPingSuffix))
}

// HasPingSupport checks if the provided protocol supports Ping protocol.
func HasPingSupport(protocol Protocol) bool {
	return slices.Contains(ProtocolsWithPingSupport, protocol)
}
