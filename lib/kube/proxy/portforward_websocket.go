/*
Copyright 2022 Gravitational, Inc.

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

package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apiserver/pkg/util/wsstream"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/utils"
)

const (
	// portForwardDataChannel is the prefix for WebSocket data channel.
	// data: [portForwardDataChannel, data...]
	portForwardDataChannel = iota
	// portForwardErrorChannel  is the prefix for WebSocket error channel.
	// error: [portForwardErrorChannel, data...]
	portForwardErrorChannel
)

// runPortForwardingWebSocket handles a request to forward ports to a pod using
// WebSocket protocol. For each port to forward, a pair of "channels" is created
// (DATA (0), ERROR (1)) when the request is upgraded and the associated port is
// written to each channel as unsigned 16 integer. It's required to identify
// which channels belong to each port.
// Due to a protocol limitation, WebSockets do not support multiplexing nor
// concurrent requests.
func runPortForwardingWebSocket(req portForwardRequest) error {
	ports, err := extractTargetPortsFromStrings(req.ports)
	if err != nil {
		return trace.Wrap(err)
	}

	// When dialing to the upstream server (Teleport or Kubernetes API server),
	// Teleport uses the SPDY implementation instead of WebSockets.
	targetConn, _, err := req.targetDialer.Dial(PortForwardProtocolV1Name)
	if err != nil {
		return trace.ConnectionProblem(err, "error dialing to upstream connection")
	}
	defer targetConn.Close()

	// One pair of (Data,Error) channels per port.
	channels := make([]wsstream.ChannelType, 2*len(ports))
	for i := 0; i < len(channels); i++ {
		channels[i] = wsstream.ReadWriteChannel
	}

	// Create a stream upgrader with protocol negotiation.
	conn := wsstream.NewConn(map[string]wsstream.ChannelProtocolConfig{
		"": {
			Binary:   true,
			Channels: channels,
		},
		v4BinaryWebsocketProtocol: {
			Binary:   true,
			Channels: channels,
		},
		v4Base64WebsocketProtocol: {
			Binary:   false,
			Channels: channels,
		},
	})
	conn.SetIdleTimeout(IdleTimeout)

	// Upgrade the request and create the virtual streams.
	_, streams, err := conn.Open(
		req.httpResponseWriter,
		req.httpRequest,
	)
	if err != nil {
		return trace.ConnectionProblem(err, "unable to upgrade websocket connection")
	}
	defer conn.Close()

	// Create the websocket stream pairs.
	streamPairs := make([]*websocketChannelPair, len(ports))
	for i := 0; i < len(ports); i++ {
		var (
			dataStream  = streams[2*i+portForwardDataChannel]
			errorStream = streams[2*i+portForwardErrorChannel]
			port        = ports[i]
		)

		streamPairs[i] = &websocketChannelPair{
			port:        port,
			dataStream:  dataStream,
			errorStream: errorStream,
			// create one requestID per pair so we can forward to multiple ports
			// correctly.
			// Since websockets do no support multiplexing, it's ok to use a single
			// request per port since users cannot send concurrent requests to
			// Kubernetes API server.
			// Although users can connect via Websocket, Teleport connection between
			// its components or Kubernetes API server is done using SPDY client
			// which requires request_id.
			requestID: fmt.Sprintf("%d", port),
			podName:   req.podName,
		}

		portBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(portBytes, port)
		// Protocol requires sending the port to identify which channels belong to
		// each port.
		if _, err := dataStream.Write(portBytes); err != nil {
			return trace.Wrap(err)
		}
		if _, err := errorStream.Write(portBytes); err != nil {
			return trace.Wrap(err)
		}
	}

	h := &websocketPortforwardHandler{
		conn:          conn,
		streamPairs:   streamPairs,
		podName:       req.podName,
		targetConn:    targetConn,
		onPortForward: req.onPortForward,
		FieldLogger: logrus.WithFields(logrus.Fields{
			trace.Component:   teleport.Component(teleport.ComponentProxyKube),
			events.RemoteAddr: req.httpRequest.RemoteAddr,
		}),
		context: req.context,
	}
	// run the portforward request until termination.
	h.run()
	return nil
}

// extractTargetPortsFromStrings extracts the desired ports from the request
// query parameters.
func extractTargetPortsFromStrings(portsStrings []string) ([]uint16, error) {
	if len(portsStrings) == 0 {
		return nil, trace.BadParameter("query parameter %q is required", PortHeader)
	}

	ports := make([]uint16, 0, len(portsStrings))
	for _, portString := range portsStrings {
		if len(portString) == 0 {
			return nil, trace.BadParameter("query parameter %q cannot be empty", PortHeader)
		}
		for _, p := range strings.Split(portString, ",") {
			port, err := parsePortString(p)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			ports = append(ports, port)
		}
	}

	return ports, nil
}

// websocketChannelPair represents the error and data streams for a single
// port.
type websocketChannelPair struct {
	port        uint16
	podName     string
	requestID   string
	dataStream  io.ReadWriteCloser
	errorStream io.ReadWriteCloser
}

func (w *websocketChannelPair) close() {
	w.dataStream.Close()
	w.errorStream.Close()
}

func (w *websocketChannelPair) sendErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(w.errorStream, "error forwarding port %d to pod %s: %v", w.port, w.podName, err)
}

// websocketPortforwardHandler is capable of processing a single port forward
// request over a websocket connection
type websocketPortforwardHandler struct {
	conn          *wsstream.Conn
	streamPairs   []*websocketChannelPair
	podName       string
	targetConn    httpstream.Connection
	onPortForward portForwardCallback
	logrus.FieldLogger
	context context.Context
}

// run invokes the targetConn SPDY connection and copies the client data into
// the targetConn and the responses into the targetConn data stream.
// If any error occurs, stream is closed an the error is sent via errorStream.
func (h *websocketPortforwardHandler) run() {
	wg := sync.WaitGroup{}
	wg.Add(len(h.streamPairs))

	for _, pair := range h.streamPairs {
		p := pair
		go func() {
			defer wg.Done()
			h.portForward(p)
		}()
	}

	wg.Wait()
}

// portForward copies the client and upstream streams.
func (h *websocketPortforwardHandler) portForward(p *websocketChannelPair) {
	h.Debugf("Forwarding port %v -> %v.", p.requestID, p.port)
	h.forwardStreamPair(p)

	h.Debugf("Completed forwarding port %v -> %v.", p.requestID, p.port)
}

func (h *websocketPortforwardHandler) forwardStreamPair(p *websocketChannelPair) {
	// create error stream
	headers := http.Header{}
	headers.Set(StreamType, StreamTypeError)
	headers.Set(PortHeader, fmt.Sprint(p.port))
	headers.Set(PortForwardRequestIDHeader, p.requestID)

	// read and write from the error stream
	targetErrorStream, err := h.targetConn.CreateStream(headers)
	h.onPortForward(fmt.Sprintf("%v:%v", h.podName, p.port), err == nil /* success */)
	if err != nil {
		p.sendErr(err)
		p.close()
		return
	}
	defer targetErrorStream.Close()
	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		if err := utils.ProxyConn(h.context, p.errorStream, targetErrorStream); err != nil {
			h.WithError(err).Debugf("Unable to proxy portforward error-stream.")
		}
	}()

	// create data stream
	headers.Set(StreamType, StreamTypeData)
	dataStream, err := h.targetConn.CreateStream(headers)
	if err != nil {
		p.sendErr(err)
		p.close()
		wg.Wait()
		return
	}
	defer dataStream.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := utils.ProxyConn(h.context, p.dataStream, dataStream); err != nil {
			h.WithError(err).Debugf("Unable to proxy portforward data-stream.")
		}
	}()

	h.Debugf("Streams have been created, Waiting for copy to complete.")
	// Wait until every goroutine exits.
	wg.Wait()

	h.Infof("Port forwarding pair completed.")
}
