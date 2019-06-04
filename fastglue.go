package fastglue

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/buaazp/fasthttprouter"
	"github.com/gorilla/schema"
	"github.com/valyala/fasthttp"
)

const (
	// JSON is an alias for the JSON content type
	JSON = "application/json"

	// XML is an alias for the XML content type
	XML = "application/xml"

	// PLAINTEXT is an alias for the plaintext content type
	PLAINTEXT = "text/plain"

	// AuthBasic represents HTTP BasicAuth scheme.
	AuthBasic = 1 << iota
	// AuthToken represents the key:value Token auth scheme.
	AuthToken = 2
)

var (
	constJSON = []byte("json")
	constXML  = []byte("xml")

	// Authorization schemes.
	authBasic = []byte("Basic")
	authToken = []byte("token")

	// Decoder for standard POST Form data decoding.
	decoder *schema.Decoder
)

// FastRequestHandler is the fastglue HTTP request handler function
// that wraps over the fasthttp handler.
type FastRequestHandler func(*Request) error

// FastMiddleware is the fastglue middleware handler function
// that can be registered using Before() and After() functions.
type FastMiddleware func(*Request) *Request

// Request is a wrapper over fasthttp's RequestCtx that's injected
// into request handlers.
type Request struct {
	RequestCtx *fasthttp.RequestCtx
	Context    interface{}
}

// Fastglue is the "glue" wrapper over fasthttp and fasthttprouter.
type Fastglue struct {
	Router      *fasthttprouter.Router
	Server      *fasthttp.Server
	context     interface{}
	contentType string

	before []FastMiddleware
	after  []FastMiddleware
}

func init() {
	// Initialise the decoder.
	decoder = schema.NewDecoder()
}

// New creates and returns a new instance of Fastglue.
func New() *Fastglue {
	return &Fastglue{
		Router: fasthttprouter.New(),
	}
}

// ListenAndServe is a wrapper for fasthttp.ListenAndServe. It takes a TCP address,
// an optional UNIX socket file path and starts listeners, and an optional fasthttp.Server.
func (f *Fastglue) ListenAndServe(address string, socket string, s *fasthttp.Server) error {
	if address == "" && socket == "" {
		return errors.New("specify either a TCP address or a UNIX socket")
	}
	if address != "" && socket != "" {
		return errors.New("specify either a TCP address or a UNIX socket, not both")
	}

	// No server passed, create a default one.
	if s == nil {
		s = &fasthttp.Server{}
	}
	f.Server = s

	if s.Handler == nil {
		s.Handler = f.Handler()
	}

	if socket != "" {
		return s.ListenAndServeUNIX(socket, 0666)
	}

	return s.ListenAndServe(address)
}

// hanlder is the "proxy" abstraction that converts a fastglue handler into
// a fasthttp handler and passes execution in and out.
func (f *Fastglue) handler(h FastRequestHandler) func(*fasthttp.RequestCtx) {
	return func(ctx *fasthttp.RequestCtx) {
		req := &Request{
			RequestCtx: ctx,
			Context:    f.context,
		}

		// Apply "before" middleware.
		for _, p := range f.before {
			if p(req) == nil {
				return
			}
		}

		h(req)

		// Apply "after" middleware.
		for _, p := range f.after {
			if p(req) == nil {
				return
			}
		}

	}
}

// Handler returns fastglue's central fasthttp handler that can be registered
// to a fasthttp server instance.
func (f *Fastglue) Handler() func(*fasthttp.RequestCtx) {
	// return fasthttp.TimeoutHandler(f.Router.Handler, f.Server.WriteTimeout, "Request timed out")
	return f.Router.Handler
}

// SetContext sets a "context" which is shared and made available in every HTTP request.
// This is useful for injecting dependencies such as config structs, DB connections etc.
// Be very careful to only include immutable variables and thread-safe objects.
func (f *Fastglue) SetContext(c interface{}) {
	f.context = c
}

// Before registers a fastglue middleware that's executed before an HTTP request
// is handed over to the registered handler. This is useful for doing "global"
// checks, for instance, session and cookies.
func (f *Fastglue) Before(fm ...FastMiddleware) {
	for _, h := range fm {
		f.before = append(f.before, h)
	}
}

// After registers a fastglue middleware that's executed after a registered handler
// has finished executing. This is useful to do things like central request logging.
func (f *Fastglue) After(fm ...FastMiddleware) {
	for _, h := range fm {
		f.after = append(f.after, h)
	}
}

// POST is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) POST(path string, h FastRequestHandler) {
	f.Router.POST(path, f.handler(h))
}

// GET is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) GET(path string, h FastRequestHandler) {
	f.Router.GET(path, f.handler(h))
}

// PUT is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) PUT(path string, h FastRequestHandler) {
	f.Router.PUT(path, f.handler(h))
}

// DELETE is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) DELETE(path string, h FastRequestHandler) {
	f.Router.DELETE(path, f.handler(h))
}

// OPTIONS is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) OPTIONS(path string, h FastRequestHandler) {
	f.Router.OPTIONS(path, f.handler(h))
}

// HEAD is fastglue's wrapper over fasthttprouter's handler.
func (f *Fastglue) HEAD(path string, h FastRequestHandler) {
	f.Router.HEAD(path, f.handler(h))
}

// Any is fastglue's wrapper over fasthttprouter's handler
// that attaches a FastRequestHandler to all
// GET, POST, PUT, DELETE methods.
func (f *Fastglue) Any(path string, h FastRequestHandler) {
	f.Router.GET(path, f.handler(h))
	f.Router.POST(path, f.handler(h))
	f.Router.PUT(path, f.handler(h))
	f.Router.DELETE(path, f.handler(h))
}

// Decode unmarshals the Post body of a fasthttp request based on the ContentType header
// into value pointed to by v, as long as the content is JSON or XML.
func (r *Request) Decode(v interface{}) error {
	var (
		err error
		ct  = r.RequestCtx.Request.Header.ContentType()
	)

	// Validate compulsory fields in JSON body. The struct to be unmarshaled into needs a struct tag with required=true for enforcing presence.
	if bytes.Contains(ct, constJSON) {
		err = json.Unmarshal(r.RequestCtx.PostBody(), &v)
		value := reflect.ValueOf(v).Elem()
		for i := 0; i < value.NumField(); i++ {
			tag := value.Type().Field(i).Tag.Get("required")
			jTagName := strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0]
			if jTagName == "" {
				jTagName = value.Type().Field(i).Name
			}
			vv := value.Field(i)
			if tag == "true" {

				if vv.Kind() == reflect.Ptr && vv.IsNil() {
					return errors.New(jTagName + " is invalid.")
				}
			}

			if tag == "nonzero" {
				switch vv.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					if vv.Int() == 0 {
						return errors.New(jTagName + " can't be zero.")
					}
				case reflect.Float32, reflect.Float64:
					if vv.Float() == 0 {
						return errors.New(jTagName + " can't be zero.")
					}
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					if vv.Uint() == 0 {
						return errors.New(jTagName + " can't be zero.")
					}
				case reflect.String:
					if vv.String() == "" {
						return errors.New(jTagName + " can't be empty.")
					}
				}
			}
		}
	} else if bytes.Contains(ct, constXML) {
		err = xml.Unmarshal(r.RequestCtx.PostBody(), &v)
	} else {
		// Try Regular POST FORM decoding if JSON/XML headers aren't set.
		// Add schema:"<schema name>,required" struct tag to the struct to be unmarshalled into.

		//Make a Map of POST Form Data.
		postDataMap := makeMapFromArgs(r.RequestCtx.PostArgs())
		// Decode map into our interface.
		err = decoder.Decode(v, postDataMap)
	}

	if err != nil {
		return errors.New("Error decoding: " + err.Error())
	}

	return nil
}

// Helper to make a Map from FastHttp POST Args.
func makeMapFromArgs(args *fasthttp.Args) map[string][]string {
	postFormMap := make(map[string][]string)
	args.VisitAll(func(k, v []byte) {
		if val, ok := postFormMap[string(k)]; !ok {
			postFormMap[string(k)] = []string{string(v)}
		} else {
			postFormMap[string(k)] = append(val, string(v))
		}
	})

	return postFormMap
}

// SendBytes writes a []byte payload to the HTTP response and also
// sets a given ContentType header.
func (r *Request) SendBytes(code int, ctype string, v []byte) error {
	r.RequestCtx.SetStatusCode(code)
	r.RequestCtx.SetContentType(ctype)
	r.RequestCtx.Write(v)

	return nil
}

// SendString writes a string payload to the HTTP response.
// It implicitly sets ContentType to plain/text.
func (r *Request) SendString(code int, v string) error {
	r.RequestCtx.SetStatusCode(code)
	r.RequestCtx.SetContentType("text/plain")
	r.RequestCtx.WriteString(v)

	return nil
}

// SendJSON takes an interface, marshals it to JSON, and writes the
// result to the HTTP response. It implicitly sets ContentType to application/json.
func (r *Request) SendJSON(code int, v interface{}) error {
	r.RequestCtx.SetStatusCode(code)
	r.RequestCtx.SetContentType(JSON)

	var (
		b   []byte
		err error
	)

	if b, err = json.Marshal(v); err == nil {
		r.RequestCtx.Write(b)
		return nil
	}

	return err
}

// Redirect redirects to the given URI.
// Accepts optional query args and anchor tags.
// Test : curl -I -L -X GET "localhost:8000/redirect"
func (r *Request) Redirect(uri string, code int, args map[string]interface{}, anchor string) error {
	var redirectURI string

	// Copy current url before mutating.
	rURI := &fasthttp.URI{}
	r.RequestCtx.URI().CopyTo(rURI)
	rURI.Update(uri)

	// Fill query args.
	for k, v := range args {
		rURI.QueryArgs().Add(k, fmt.Sprintf("%v", v))
	}

	// With layered proxies and loadbalancers, redirect
	// to relative URLs may not work correctly, that is,
	// the load balancer entry point was https but at the
	// end of the proxy chain, it's http.
	// So we check if the incoming hostname and the outgoing
	// redirect URL's hostname are the same, and if yes,
	// check for common scheme headers and overwrite the
	// scheme if they are set.
	if bytes.Equal(r.RequestCtx.Host(), rURI.Host()) {
		s := r.RequestCtx.Request.Header.Peek("X-Forwarded-Proto")
		if len(s) > 0 {
			rURI.SetScheme(string(s))
		}
	}

	redirectURI = rURI.String()
	// If anchor is sent, append to the URI.
	if anchor != "" {
		redirectURI += "#" + anchor
	}

	// Redirect
	r.RequestCtx.Redirect(redirectURI, code)
	return nil
}

// ParseAuthHeader parses the Authorization header and returns an api_key and access_token
// based on the auth schemes passed as bit flags (eg: AuthBasic, AuthBasic | AuthToken etc.).
func (r *Request) ParseAuthHeader(schemes uint8) ([]byte, []byte, error) {
	var (
		h     = r.RequestCtx.Request.Header.Peek("Authorization")
		pair  [][]byte
		delim = []byte(":")
	)

	// Basic auth scheme.
	if schemes&AuthBasic != 0 && bytes.HasPrefix(h, authBasic) {
		payload, err := base64.StdEncoding.DecodeString(string(bytes.Trim(h[len(authBasic):], " ")))
		if err != nil {
			return nil, nil, errors.New("invalid Base64 value in Basic Authorization header")
		}

		pair = bytes.SplitN(payload, delim, 2)
	} else if schemes&AuthToken != 0 && bytes.HasPrefix(h, authToken) {
		pair = bytes.SplitN(bytes.Trim(h[len(authToken):], " "), delim, 2)
	} else {
		return nil, nil, errors.New("unknown authorization scheme")
	}

	if len(pair) != 2 {
		return nil, nil, errors.New("authorization value should be `key`:`token`")
	}

	return pair[0], pair[1], nil
}
