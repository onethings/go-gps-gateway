package codec

import "net/url"

// HTTPRequest is the protocol independent view of one HTTP request. A protocol
// that devices reach over HTTP reads the query or the body from here instead of
// touching the net/http types, so its decoder stays testable without a socket.
type HTTPRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// HTTPResponse is what an HTTP protocol answers with. A zero Status means 200
// and an empty ContentType means "text/plain; charset=utf-8".
type HTTPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// HTTPDecoder is implemented by the protocols that are served over HTTP. The
// gateway turns each request into one call and writes the response back.
type HTTPDecoder interface {
	// Name returns the protocol name, e.g. "osmand".
	Name() string
	// DecodeHTTP parses one request. A nil request result with a nil error means
	// the frame produced no position, which most HTTP devices accept silently.
	DecodeHTTP(request *HTTPRequest) (*Result, *HTTPResponse, error)
}
