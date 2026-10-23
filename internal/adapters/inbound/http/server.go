package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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
}

// ErrNoIdempotency is returned by NewRouter when the Server has no
// Idempotency middleware: there is no unkeyed mode.
var ErrNoIdempotency = errors.New("http server: Idempotency is required (every state-changing route takes an Idempotency-Key)")

// NewRouter builds the chi router. Order of the middleware, outermost first:
// access log, recover (a panic is a 500 problem the log still sees), rejectNUL
// (before anything is recorded), then the route; on a state-changing route the
// Idempotency-Key middleware wraps the handler.
func NewRouter(s *Server) (http.Handler, error) {
	if s.Idempotency == nil {
		return nil, ErrNoIdempotency
	}
	r := chi.NewRouter()
	r.Use(accessLog(s.Logger), recoverer, rejectNUL)
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

// accessLog logs one line per request with its status and duration.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			logger.LogAttrs(r.Context(), levelFor(r.URL.Path, status), "request",
				slog.String("method", r.Method), slog.String("path", r.URL.EscapedPath()),
				slog.Int("status", status), slog.Duration("duration", time.Since(start)))
		})
	}
}

// levelFor keeps the probes quiet and flags server failures.
func levelFor(path string, status int) slog.Level {
	if status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	if path == "/healthz" || path == "/readyz" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
