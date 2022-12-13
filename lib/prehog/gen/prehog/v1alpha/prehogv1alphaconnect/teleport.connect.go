// Code generated by protoc-gen-connect-go. DO NOT EDIT.
//
// Source: prehog/v1alpha/teleport.proto

package prehogv1alphaconnect

import (
	context "context"
	errors "errors"
	connect_go "github.com/bufbuild/connect-go"
	v1alpha "github.com/zmb3/teleport/lib/prehog/gen/prehog/v1alpha"
	http "net/http"
	strings "strings"
)

// This is a compile-time assertion to ensure that this generated file and the connect package are
// compatible. If you get a compiler error that this constant is not defined, this code was
// generated with a version of connect newer than the one compiled into your binary. You can fix the
// problem by either regenerating this code with an older version of connect or updating the connect
// version compiled into your binary.
const _ = connect_go.IsAtLeastVersion0_1_0

const (
	// TeleportReportingServiceName is the fully-qualified name of the TeleportReportingService service.
	TeleportReportingServiceName = "prehog.v1alpha.TeleportReportingService"
)

// TeleportReportingServiceClient is a client for the prehog.v1alpha.TeleportReportingService
// service.
type TeleportReportingServiceClient interface {
	SubmitEvent(context.Context, *connect_go.Request[v1alpha.SubmitEventRequest]) (*connect_go.Response[v1alpha.SubmitEventResponse], error)
	HelloTeleport(context.Context, *connect_go.Request[v1alpha.HelloTeleportRequest]) (*connect_go.Response[v1alpha.HelloTeleportResponse], error)
}

// NewTeleportReportingServiceClient constructs a client for the
// prehog.v1alpha.TeleportReportingService service. By default, it uses the Connect protocol with
// the binary Protobuf Codec, asks for gzipped responses, and sends uncompressed requests. To use
// the gRPC or gRPC-Web protocols, supply the connect.WithGRPC() or connect.WithGRPCWeb() options.
//
// The URL supplied here should be the base URL for the Connect or gRPC server (for example,
// http://api.acme.com or https://acme.com/grpc).
func NewTeleportReportingServiceClient(httpClient connect_go.HTTPClient, baseURL string, opts ...connect_go.ClientOption) TeleportReportingServiceClient {
	baseURL = strings.TrimRight(baseURL, "/")
	return &teleportReportingServiceClient{
		submitEvent: connect_go.NewClient[v1alpha.SubmitEventRequest, v1alpha.SubmitEventResponse](
			httpClient,
			baseURL+"/prehog.v1alpha.TeleportReportingService/SubmitEvent",
			opts...,
		),
		helloTeleport: connect_go.NewClient[v1alpha.HelloTeleportRequest, v1alpha.HelloTeleportResponse](
			httpClient,
			baseURL+"/prehog.v1alpha.TeleportReportingService/HelloTeleport",
			opts...,
		),
	}
}

// teleportReportingServiceClient implements TeleportReportingServiceClient.
type teleportReportingServiceClient struct {
	submitEvent   *connect_go.Client[v1alpha.SubmitEventRequest, v1alpha.SubmitEventResponse]
	helloTeleport *connect_go.Client[v1alpha.HelloTeleportRequest, v1alpha.HelloTeleportResponse]
}

// SubmitEvent calls prehog.v1alpha.TeleportReportingService.SubmitEvent.
func (c *teleportReportingServiceClient) SubmitEvent(ctx context.Context, req *connect_go.Request[v1alpha.SubmitEventRequest]) (*connect_go.Response[v1alpha.SubmitEventResponse], error) {
	return c.submitEvent.CallUnary(ctx, req)
}

// HelloTeleport calls prehog.v1alpha.TeleportReportingService.HelloTeleport.
func (c *teleportReportingServiceClient) HelloTeleport(ctx context.Context, req *connect_go.Request[v1alpha.HelloTeleportRequest]) (*connect_go.Response[v1alpha.HelloTeleportResponse], error) {
	return c.helloTeleport.CallUnary(ctx, req)
}

// TeleportReportingServiceHandler is an implementation of the
// prehog.v1alpha.TeleportReportingService service.
type TeleportReportingServiceHandler interface {
	SubmitEvent(context.Context, *connect_go.Request[v1alpha.SubmitEventRequest]) (*connect_go.Response[v1alpha.SubmitEventResponse], error)
	HelloTeleport(context.Context, *connect_go.Request[v1alpha.HelloTeleportRequest]) (*connect_go.Response[v1alpha.HelloTeleportResponse], error)
}

// NewTeleportReportingServiceHandler builds an HTTP handler from the service implementation. It
// returns the path on which to mount the handler and the handler itself.
//
// By default, handlers support the Connect, gRPC, and gRPC-Web protocols with the binary Protobuf
// and JSON codecs. They also support gzip compression.
func NewTeleportReportingServiceHandler(svc TeleportReportingServiceHandler, opts ...connect_go.HandlerOption) (string, http.Handler) {
	mux := http.NewServeMux()
	mux.Handle("/prehog.v1alpha.TeleportReportingService/SubmitEvent", connect_go.NewUnaryHandler(
		"/prehog.v1alpha.TeleportReportingService/SubmitEvent",
		svc.SubmitEvent,
		opts...,
	))
	mux.Handle("/prehog.v1alpha.TeleportReportingService/HelloTeleport", connect_go.NewUnaryHandler(
		"/prehog.v1alpha.TeleportReportingService/HelloTeleport",
		svc.HelloTeleport,
		opts...,
	))
	return "/prehog.v1alpha.TeleportReportingService/", mux
}

// UnimplementedTeleportReportingServiceHandler returns CodeUnimplemented from all methods.
type UnimplementedTeleportReportingServiceHandler struct{}

func (UnimplementedTeleportReportingServiceHandler) SubmitEvent(context.Context, *connect_go.Request[v1alpha.SubmitEventRequest]) (*connect_go.Response[v1alpha.SubmitEventResponse], error) {
	return nil, connect_go.NewError(connect_go.CodeUnimplemented, errors.New("prehog.v1alpha.TeleportReportingService.SubmitEvent is not implemented"))
}

func (UnimplementedTeleportReportingServiceHandler) HelloTeleport(context.Context, *connect_go.Request[v1alpha.HelloTeleportRequest]) (*connect_go.Response[v1alpha.HelloTeleportResponse], error) {
	return nil, connect_go.NewError(connect_go.CodeUnimplemented, errors.New("prehog.v1alpha.TeleportReportingService.HelloTeleport is not implemented"))
}
