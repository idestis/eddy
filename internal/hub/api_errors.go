package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/idestis/eddy/internal/ai"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/threads"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the docs/api.md error envelope.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// httpError is the HTTP form of an error.
type httpError struct {
	status  int
	code    string
	message string
}

// classify maps service errors to the docs/api.md codes. Only client
// errors carry the error text; server errors get a generic message.
func classify(err error) httpError {
	msg := err.Error()
	var ae *AgentError
	switch {
	case errors.Is(err, fleet.ErrConfirmRequired):
		return httpError{http.StatusPreconditionRequired, "confirm_required", msg}
	case errors.Is(err, fleet.ErrDisabled):
		return httpError{http.StatusServiceUnavailable, "disabled", "this feature is disabled"}
	case errors.Is(err, fleet.ErrDisconnected):
		return httpError{http.StatusServiceUnavailable, "disconnected", msg}
	case errors.Is(err, fleet.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return httpError{http.StatusNotFound, "not_found", msg}
	case errors.Is(err, fleet.ErrForbidden):
		return httpError{http.StatusForbidden, "forbidden", msg}
	case errors.Is(err, ai.ErrRateLimited):
		return httpError{http.StatusTooManyRequests, "rate_limited", msg}
	case isMaxBytes(err):
		return httpError{http.StatusRequestEntityTooLarge, "bad_request", "request body too large"}
	case errors.Is(err, ErrBadRequest), errors.Is(err, threads.ErrInvalid), errors.Is(err, store.ErrInvalid),
		errors.Is(err, ai.ErrInvalid):
		return httpError{http.StatusBadRequest, "bad_request", msg}
	case errors.Is(err, store.ErrLimit), errors.Is(err, store.ErrConflict):
		return httpError{http.StatusConflict, "conflict", msg}
	case errors.As(err, &ae) && ae.Code == http.StatusConflict:
		return httpError{http.StatusConflict, "conflict", msg}
	case errors.Is(err, errUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ai.ErrUnavailable):
		return httpError{http.StatusServiceUnavailable, "unavailable", msg}
	}
	return httpError{http.StatusInternalServerError, "internal", "internal error"}
}

// fail writes err as an API error, logging server-side failures.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // the client went away
	}
	he := classify(err)
	if he.status >= 500 {
		a.log.LogAttrs(r.Context(), slog.LevelError, "request failed",
			slog.String("path", r.URL.Path), slog.Int("status", he.status), slog.String("err", err.Error()))
	}
	writeError(w, he.status, he.code, he.message)
}

// decodeJSON reads a JSON body of at most limit bytes into v. An empty body
// leaves v unchanged when allowEmpty is set.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any, allowEmpty bool) error {
	body := http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) && allowEmpty {
			return nil
		}
		if isMaxBytes(err) {
			return err
		}
		return badRequest("invalid JSON body: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if dec.More() {
		return badRequest("invalid JSON body: trailing data")
	}
	return nil
}

func notFoundAPI(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
}
