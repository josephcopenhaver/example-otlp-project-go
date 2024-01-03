package router

import (
	"net/http"
	"strings"
	"sync"

	"github.com/josephcopenhaver/xit/xnet/xhttp"
	"github.com/julienschmidt/httprouter"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Middleware = func(http.Handler) http.Handler

type Router struct {
	r                        *httprouter.Router
	cleanupFuncs             []func()
	cleanupFuncsM            sync.Mutex
	errHandler               ErrHandler
	beforeRoutingMiddlewares []Middleware
	afterRoutingMiddlewares  []Middleware
	handler                  http.Handler
}

func DefaultNotFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, ignoredErr := w.Write([]byte(`{}` + "\n"))
	_ = ignoredErr
}

func DefaultMethodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, ignoredErr := w.Write([]byte(`{}` + "\n"))
	_ = ignoredErr
}

func DefaultPanicHandler(w http.ResponseWriter, r *http.Request, p any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, ignoredErr := w.Write([]byte(`{}` + "\n"))
	_ = ignoredErr
}

const rootServerOperationName = "http.Request"

// TODO: use options pattern
func New(errHandler ErrHandler, notFoundHandler, methodNotAllowedHandler http.Handler, panicHandler func(http.ResponseWriter, *http.Request, any)) *Router {
	if errHandler == nil {
		errHandler = DefaultErrHandler
	}

	r := httprouter.New()
	r.HandleOPTIONS = false
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false

	if notFoundHandler == nil {
		notFoundHandler = http.HandlerFunc(DefaultNotFoundHandler)
	}
	r.NotFound = otelhttp.NewHandler(notFoundHandler, rootServerOperationName)

	if methodNotAllowedHandler == nil {
		methodNotAllowedHandler = http.HandlerFunc(DefaultMethodNotAllowedHandler)
	}
	r.MethodNotAllowed = otelhttp.NewHandler(methodNotAllowedHandler, rootServerOperationName)

	if panicHandler == nil {
		panicHandler = DefaultPanicHandler
	}
	r.PanicHandler = panicHandler

	tracemw, err := xhttp.NewTraceMiddleware(xhttp.TraceMiddlewareOpts().UsePool(true))
	if err != nil {
		panic(err)
	}

	return &Router{
		errHandler: errHandler,
		beforeRoutingMiddlewares: []Middleware{
			func(next http.Handler) http.Handler {
				return otelhttp.NewHandler(next, rootServerOperationName)
			},
			tracemw,
		},
		r: r,
	}
}

func (ro *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ro.handler.ServeHTTP(w, r)
}

func (ro *Router) AppendAfterRoutingMiddleware(mws ...Middleware) {
	ro.afterRoutingMiddlewares = append(ro.afterRoutingMiddlewares, mws...)
}

func (ro *Router) SetHandler() {
	h := http.Handler(ro.r)

	for i := len(ro.beforeRoutingMiddlewares) - 1; i >= 0; i-- {
		h = ro.beforeRoutingMiddlewares[i](h)
	}

	ro.handler = h
}

func (ro *Router) RegisterCleanup(f ...func()) {
	ro.cleanupFuncsM.Lock()
	defer ro.cleanupFuncsM.Unlock()

	ro.cleanupFuncs = append(ro.cleanupFuncs, f...)
}

func (ro *Router) cleanupFunc() func() {
	ro.cleanupFuncsM.Lock()
	defer ro.cleanupFuncsM.Unlock()

	cf := ro.cleanupFuncs
	if len(cf) == 0 {
		return func() {}
	}

	ro.cleanupFuncs = nil

	return func() {
		var wg sync.WaitGroup
		wg.Add(len(cf))

		for _, f := range cf {
			f := f
			go func() {
				defer wg.Done()
				f()
			}()
		}

		wg.Wait()
	}
}

func (ro *Router) Cleanup() {
	f := ro.cleanupFunc()
	f()
}

func (ro *Router) handle(method string, path string, h http.Handler) {
	for i := len(ro.afterRoutingMiddlewares) - 1; i >= 0; i-- {
		h = ro.afterRoutingMiddlewares[i](h)
	}

	// instead of just
	// `ro.r.Handler(method, path, h)`
	//
	// going to make the service agnostic to single trailing slashes

	// TODO: this should be the first layer wrapping everything, not the last, ugh
	nh := otelhttp.WithRouteTag(method+" "+path, h)

	path = strings.TrimRight(path, "/")
	ro.r.Handler(method, path, nh)
	ro.r.Handler(method, path+"/", nh)
}

func (ro *Router) handleE(method string, path string, h HandlerExt) {
	ro.handle(method, path, ro.errHandler(h))
}

func (ro *Router) GetE(path string, h HandlerExt) {
	ro.handleE(http.MethodGet, path, h)
}
