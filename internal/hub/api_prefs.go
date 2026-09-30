package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// maxPrefsBytes is the largest accepted preferences object (the store's limit).
const maxPrefsBytes = 16 << 10

type prefsBody struct {
	Data json.RawMessage `json:"data"`
}

// handlePrefs returns the signed-in user's UI preferences, {} when none.
func (a *api) handlePrefs(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	raw, err := a.store.Prefs().Get(r.Context(), p.User)
	if errors.Is(err, store.ErrNotFound) {
		raw, err = json.RawMessage(`{}`), nil
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, prefsBody{Data: raw})
}

// handlePutPrefs replaces the signed-in user's UI preferences.
func (a *api) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body prefsBody
	if err := decodeJSON(w, r, maxPrefsBytes+1024, &body, false); err != nil {
		a.fail(w, r, err)
		return
	}
	data := bytes.TrimSpace(body.Data)
	if len(data) == 0 || data[0] != '{' {
		a.fail(w, r, badRequest("data must be a JSON object"))
		return
	}
	if len(data) > maxPrefsBytes {
		a.fail(w, r, &http.MaxBytesError{Limit: maxPrefsBytes})
		return
	}
	if err := a.store.Prefs().Put(r.Context(), p.User, data); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, prefsBody{Data: data})
}
