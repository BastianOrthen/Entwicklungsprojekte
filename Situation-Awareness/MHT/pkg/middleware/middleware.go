// Package middleware contains composable net/http middleware used by all
// SAW services.  Use middleware.Chain to stack them in a readable order.
package middleware

import "net/http"

// Chain composes middleware so that the first middleware in the slice runs
// first (outermost).  Example:
//
//	h := middleware.Chain(mux,
//	    middleware.Recover(logger),
//	    middleware.RequestLogger(logger),
//	    middleware.CORS([]string{"*"}),
//	)
func Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
