package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/offer-availability/internal/adapters/inbound/requestlog"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
)

// The use-case contracts the handlers call. *usecases.X satisfy them; the
// handler tests substitute use cases over in-memory doubles.
type (
	createCatalogItem interface {
		Execute(context.Context, usecases.CreateCatalogItemCommand) (views.CatalogItem, error)
	}
	discontinueItem interface {
		Execute(context.Context, catalog.ID) (views.CatalogItem, error)
	}
	browseCatalog interface {
		Execute(context.Context, usecases.PageQuery) (views.CatalogPage, error)
	}
	getCatalogItem interface {
		Execute(context.Context, catalog.ID) (views.CatalogItem, error)
	}
	createListing interface {
		Execute(context.Context, usecases.CreateListingCommand) (views.Listing, error)
	}
	listListings interface {
		Execute(context.Context, usecases.ListListingsQuery) (views.ListingPage, error)
	}
	declarePackLevel interface {
		Execute(context.Context, usecases.DeclarePackLevelCommand) (views.CatalogItem, error)
	}
	retirePackLevel interface {
		Execute(context.Context, catalog.ID, string) (views.CatalogItem, error)
	}
	applyInventoryFeed interface {
		Execute(context.Context, usecases.ApplyInventoryFeedCommand) (views.InventoryFeed, error)
	}
	getInventoryFeed interface {
		Execute(context.Context, feed.ID) (views.InventoryFeed, error)
	}
)

// Server holds everything the router needs; cmd/api fills every field.
type Server struct {
	CreateCatalogItem  createCatalogItem
	DiscontinueItem    discontinueItem
	DeclarePackLevel   declarePackLevel
	RetirePackLevel    retirePackLevel
	BrowseCatalog      browseCatalog
	GetCatalogItem     getCatalogItem
	CreateListing      createListing
	ListListings       listListings
	ApplyInventoryFeed applyInventoryFeed
	GetInventoryFeed   getInventoryFeed

	// Idempotency guards every state-changing route; it is mandatory.
	Idempotency Idempotency
	// Readiness backs /readyz; the zero gate is ready with no dependency.
	Readiness *Readiness
	// Logger receives the access log; slog.Default when nil.
	Logger *slog.Logger
	// ServiceName names the server spans and the HTTP metrics (OTEL_SERVICE_NAME
	// of the composition root); DefaultServiceName when empty.
	ServiceName string
}

// DefaultServiceName names the server spans and the HTTP metrics when the
// composition root passes no service name.
const DefaultServiceName = "offer-availability"

// ErrNoIdempotency is returned by NewRouter when the Server has no
// Idempotency middleware: there is no unkeyed mode.
var ErrNoIdempotency = errors.New("http server: Idempotency is required (every state-changing route takes an Idempotency-Key)")

// NewRouter builds the chi router. Order of the middleware, outermost first, is
// the fleet observability standard: RequestID, then otelchi (the server span,
// named after the chi route pattern, so one span name per route and not per
// catalogue item id), then the request-duration histogram, then RequestLogger
// (it runs inside the span, so its record carries trace_id and span_id), then
// the recoverer (a panic is a 500 problem the log still sees); rejectNUL
// follows (before anything is recorded), then the route; on a state-changing
// route the Idempotency-Key middleware wraps the handler. telemetry.Setup must
// have run before this call: otelchimetric binds the global MeterProvider when
// it is constructed.
func NewRouter(s *Server) (http.Handler, error) {
	if s.Idempotency == nil {
		return nil, ErrNoIdempotency
	}
	serviceName := s.ServiceName
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(s.Logger))
	r.Use(recoverer)
	r.Use(rejectNUL)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, routeNotFound, "no route matches "+r.Method+" "+r.URL.EscapedPath())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, methodNotAllowed, r.Method+" is not allowed on "+r.URL.EscapedPath())
	})

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", readyz(s.Readiness))

	s.mountCatalog(r)
	s.mountListings(r)
	s.mountFeeds(r)
	return r, nil
}

// keyed registers a state-changing route behind the Idempotency-Key
// middleware. The route string ("POST /listings") is the idempotency scope.
func (s *Server) keyed(r chi.Router, method, pattern string, h http.HandlerFunc) {
	r.With(s.Idempotency(method+" "+pattern)).MethodFunc(method, pattern, h)
}

// recoverer turns a panic into a 500 problem and logs it.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared by identity on purpose
					panic(p)
				}
				slog.ErrorContext(r.Context(), "panic in handler", "panic", p, "method", r.Method, "path", r.URL.Path)
				writeProblem(w, r, internalProblem, "an unexpected error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
