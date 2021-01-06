module github.com/gravitational/teleport/api

go 1.15

replace (
	github.com/coreos/go-oidc => github.com/gravitational/go-oidc v0.0.3
	github.com/gogo/protobuf => github.com/gravitational/protobuf v1.3.2-0.20201123192827-2b9fcfaffcbf
	github.com/gravitational/teleport => ../
	github.com/iovisor/gobpf => github.com/gravitational/gobpf v0.0.1
)

require (
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.3
	github.com/gravitational/teleport v0.0.0-00010101000000-000000000000
	github.com/gravitational/trace v1.1.13
	golang.org/x/net v0.0.0-20201224014010-6772e930b67b
	google.golang.org/grpc v1.34.0
)
