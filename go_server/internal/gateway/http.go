package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"traccar-server_go/go_server/internal/codec"
)

// httpReadHeaderTimeout caps how long an HTTP device may take to send its
// request line and headers.
const httpReadHeaderTimeout = 10 * time.Second

// ServeHTTP serves one HTTP based protocol on an already bound listener.
func (s *Server) ServeHTTP(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           s.HTTPHandler(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// HTTPHandler exposes the protocol as an http.Handler. The gateway mounts it on
// a listener, and a test drives it with a recorder instead of a socket.
func (s *Server) HTTPHandler() http.Handler {
	handler := &httpProtocolHandler{server: s}
	return http.HandlerFunc(handler.serve)
}

type httpProtocolHandler struct {
	server *Server
}

func (h *httpProtocolHandler) serve(writer http.ResponseWriter, request *http.Request) {
	decoder, err := h.server.httpDecoder()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotImplemented)
		return
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, int64(h.server.maxFrameSize)))
	if err != nil {
		http.Error(writer, "read body", http.StatusBadRequest)
		return
	}
	parsed, _ := url.ParseQuery(string(body))

	result, response, err := decoder.DecodeHTTP(&codec.HTTPRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  mergeQuery(request.URL.Query(), parsed),
		Body:   body,
	})
	if err != nil {
		if h.server.decodeErrors != nil {
			h.server.decodeErrors.Inc()
		}
		h.server.logf("%s: %v", h.server.protocolName, err)
		http.Error(writer, "decode", http.StatusBadRequest)
		return
	}

	if result != nil {
		// An HTTP device sends its identifier with every request, so there is no
		// session to carry over and no connection to answer on.
		var session *codec.DeviceSession
		if err := h.server.processResult(request.Context(), nil, result, &session); err != nil {
			h.server.logf("%s: %v", h.server.protocolName, err)
		}
	}

	if response == nil {
		response = &codec.HTTPResponse{}
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := response.ContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.WriteHeader(status)
	if len(response.Body) > 0 {
		_, _ = writer.Write(response.Body)
	}
}

// httpDecoder builds the decoder of this server's protocol. A protocol without
// an HTTP path is a configuration error, not a device error.
func (s *Server) httpDecoder() (codec.HTTPDecoder, error) {
	protocol := s.factory(s.base)
	if protocol.NewHTTP == nil {
		return nil, fmt.Errorf("gateway: %s is not an HTTP protocol", s.protocolName)
	}
	return protocol.NewHTTP(s.base), nil
}

// mergeQuery combines the query string with a form encoded body, giving the
// query string precedence: OsmAnd clients send everything in the URL.
func mergeQuery(query, body url.Values) url.Values {
	merged := make(url.Values, len(query)+len(body))
	for key, values := range body {
		merged[key] = append([]string(nil), values...)
	}
	for key, values := range query {
		merged[key] = append([]string(nil), values...)
	}
	return merged
}
