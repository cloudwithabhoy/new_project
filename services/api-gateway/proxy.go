package main

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// route describes how an incoming /api/<x>/... request maps to an upstream.
type route struct {
	// prefix is the gateway path prefix, e.g. "/api/cart". It is ALSO the RED
	// metrics route label (bounded cardinality — the prefix, not the full path).
	prefix string
	// target is the upstream base URL the request is forwarded to.
	target *url.URL
	// proxy is the configured reverse proxy for this upstream.
	proxy *httputil.ReverseProxy
	// protected requires a valid Bearer JWT before proxying; on success the
	// gateway injects X-User-Id for the upstream.
	protected bool
}

// Router holds the ordered route table and the JWT verifier.
type Router struct {
	routes   []route
	verifier *Verifier
}

// routeSpec is the static prefix->upstream wiring. Order matters only for
// readability; prefixes are mutually exclusive. protected prefixes require auth.
type routeSpec struct {
	prefix    string
	rawURL    string
	protected bool
}

// NewRouter builds the gateway router from config. Each entry strips its /api/<x>
// prefix so e.g. /api/cart/carts/5 -> {CART_URL}/carts/5 and /api/users/123 ->
// {USER_URL}/users/123 (the trailing path after the prefix is preserved verbatim).
func NewRouter(cfg Config, verifier *Verifier) (*Router, error) {
	specs := []routeSpec{
		{"/api/auth", cfg.AuthURL, false},        // public
		{"/api/products", cfg.CatalogURL, false}, // public (catalog)
		{"/api/orders", cfg.OrderURL, true},      // PROTECTED
	}

	rt := &Router{verifier: verifier}
	for _, s := range specs {
		target, err := url.Parse(s.rawURL)
		if err != nil {
			return nil, err
		}
		rt.routes = append(rt.routes, route{
			prefix:    s.prefix,
			target:    target,
			proxy:     newReverseProxy(s.prefix, target),
			protected: s.protected,
		})
	}
	return rt, nil
}

// newReverseProxy builds a ReverseProxy that forwards to target after stripping
// the gateway prefix from the request path. httputil.ReverseProxy copies the
// incoming headers to the upstream by default, so the W3C/B3 trace-context
// headers propagate automatically — we must only avoid clobbering them.
func newReverseProxy(prefix string, target *url.URL) *httputil.ReverseProxy {
	p := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Route to the upstream host.
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Strip the /api/<x> prefix. /api/cart/carts/5 -> /carts/5 ;
			// /api/users/123 -> /users/123 ; /api/orders -> "" -> "/".
			rest := strings.TrimPrefix(req.URL.Path, prefix)
			if rest == "" {
				rest = "/"
			}
			// Respect any base path on the upstream URL (usually none).
			req.URL.Path = singleJoiningSlash(target.Path, rest)

			// Standard reverse-proxy hygiene: identify the proxy chain.
			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "")
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			loggerFrom(r.Context()).Error("upstream proxy error",
				"upstream", target.String(), "path", r.URL.Path, "error", err)
			writeError(w, http.StatusBadGateway, "upstream unavailable")
		},
	}
	return p
}

// match returns the route whose prefix matches the request path, longest-prefix
// first so /api/products doesn't shadow a hypothetical /api/products-foo. A path
// matches a prefix only at a segment boundary (exact, or followed by '/').
func (rt *Router) match(path string) (route, bool) {
	var best route
	found := false
	for _, r := range rt.routes {
		if path == r.prefix || strings.HasPrefix(path, r.prefix+"/") {
			if !found || len(r.prefix) > len(best.prefix) {
				best = r
				found = true
			}
		}
	}
	return best, found
}

// ServeHTTP is the gateway's proxy entrypoint: match a route, enforce auth on
// protected prefixes, then reverse-proxy to the upstream.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rte, ok := rt.match(r.URL.Path)
	if !ok {
		setRoute(r, "unknown")
		writeError(w, http.StatusNotFound, "no route for path")
		return
	}

	// Metrics route label is the upstream PREFIX (bounded cardinality).
	setRoute(r, rte.prefix)

	if rte.protected {
		sub, err := rt.authenticate(r)
		if err != nil {
			loggerFrom(r.Context()).Warn("auth rejected", "prefix", rte.prefix, "error", err)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Inject the authenticated user id for the upstream. Strip any
		// client-supplied X-User-Id first so it can't be spoofed.
		r.Header.Del("X-User-Id")
		r.Header.Set("X-User-Id", sub)
	}

	rte.proxy.ServeHTTP(w, r)
}

// authenticate verifies the Bearer token on a protected request and returns the
// subject (user id). Any failure -> 401 at the call site.
func (rt *Router) authenticate(r *http.Request) (string, error) {
	token := bearerToken(r)
	if token == "" {
		return "", errMissingToken
	}
	return rt.verifier.Verify(r.Context(), token)
}

var errMissingToken = errProxy("missing bearer token")

// errProxy is a tiny string error type so we avoid importing errors just here.
type errProxy string

func (e errProxy) Error() string { return string(e) }

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// logRoutes prints the active routing table at startup for operator visibility.
func (rt *Router) logRoutes() {
	for _, r := range rt.routes {
		slog.Info("route registered",
			"prefix", r.prefix, "upstream", r.target.String(), "protected", r.protected)
	}
}
