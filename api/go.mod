module github.com/gravitational/teleport/api

go 1.18

require (
	github.com/go-piv/piv-go v1.10.0
	github.com/gogo/protobuf v1.3.2
	github.com/google/go-cmp v0.5.9
	github.com/gravitational/trace v1.1.19
	github.com/jonboulle/clockwork v0.3.0
	github.com/russellhaering/gosaml2 v0.8.1
	github.com/sirupsen/logrus v1.9.0
	github.com/stretchr/testify v1.8.1
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.37.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.37.0
	go.opentelemetry.io/otel v1.11.2
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.11.2
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.11.2
	go.opentelemetry.io/otel/sdk v1.11.2
	go.opentelemetry.io/otel/trace v1.11.2
	go.opentelemetry.io/proto/otlp v0.19.0
	golang.org/x/exp v0.0.0-20221126150942-6ab00d035af9
	golang.org/x/net v0.4.0
	google.golang.org/grpc v1.51.0
	google.golang.org/protobuf v1.28.1
	gopkg.in/yaml.v2 v2.4.0
)

// DO NOT UPDATE until Teleport supports >= firestore 1.9.0
require google.golang.org/genproto v0.0.0-20221118155620-16455021b5e6

// DO NOT UPDATE crypto beyond v0.2.0, the commit below breaks compatibility
// with OpenSSH <= 7.6.
// We are working on landing a patch upstream.
// https://github.com/golang/crypto/commit/6fad3dfc18918c2ac9c112e46b32473bd2e5e2f9
require golang.org/x/crypto v0.2.0 // DO NOT UPDATE

require (
	cloud.google.com/go/compute/metadata v0.2.1 // indirect
	github.com/beevik/etree v1.1.0 // indirect
	github.com/cenkalti/backoff/v4 v4.2.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/felixge/httpsnoop v1.0.3 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.7.0 // indirect
	github.com/mattermost/xml-roundtrip-validator v0.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/russellhaering/goxmldsig v1.2.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/internal/retry v1.11.2 // indirect
	go.opentelemetry.io/otel/metric v0.34.0 // indirect
	golang.org/x/sys v0.3.0 // indirect
	golang.org/x/term v0.3.0 // indirect
	golang.org/x/text v0.5.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
