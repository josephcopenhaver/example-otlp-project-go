package router

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

type HandlerExt = func(http.ResponseWriter, *http.Request) error

type ErrHandler = func(h HandlerExt) http.HandlerFunc

var ErrPanicking = errors.New("panicking") // TODO: use a type that implements ServeHTTP

func DefaultErrHandler(h HandlerExt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error

		defer func() {
			rec := recover()
			if rec != nil {
				err = ErrPanicking
			}

			if err == nil {
				return
			}

			// TODO: add stack trace to error if not already present (does not implement fmt.Formatter) and set err to rec if it is an error

			if rec != nil {
				slog.ErrorContext(r.Context(),
					"error in handler",
					"error", err,
					"recover", rec,
					"trace", string(debug.Stack()),
				)
			} else {
				slog.ErrorContext(r.Context(),
					"error in handler",
					"error", err,
				)
			}

			// TODO: short circuit if headers were already sent or response body send was started
			// just use a middleware to detect it
			if v, ok := err.(http.Handler); ok {
				v.ServeHTTP(w, r)
				return
			}

			w.WriteHeader(http.StatusInternalServerError)

			// TODO: write a json error response payload as well if the service is a json service
		}()

		err = h(w, r)
		// TODO: if err is null and no response was written, log a warning
	}
}
