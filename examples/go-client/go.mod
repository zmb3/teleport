module go-client

go 1.15

replace github.com/gravitational/teleport/api/v6 v6.0.0 => ../../api

require (
	github.com/gravitational/teleport/api/v6 v6.0.0
	github.com/pborman/uuid v1.2.1
)
