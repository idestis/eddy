package hub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/identity"
)

// List ETags (ADR-0006). A list response carries a weak ETag derived from
// the data version, the caller's identity (user and exact groups), the
// request and a 45 s time bucket, so If-None-Match is answered 304 without
// filtering anything. The bucket bounds how long a validator outlives a
// permission change: at most etagBucket plus the SAR cache's accessTTL,
// 90 s, the same bound the SAR cache already has. ETags never repeat across
// users, so a shared cache cannot serve one user's list to another, and
// the responses say Cache-Control: private, no-cache.
const etagBucket = 45 * time.Second

// listETag returns the weak validator of a list response whose data is at
// version for p.
func listETag(p identity.Principal, r *http.Request, version string, now time.Time) string {
	h := sha256.New()
	h.Write([]byte(subjectKey(p)))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write([]byte(canonicalQuery(r.URL.Query())))
	h.Write([]byte{0})
	h.Write([]byte(version))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(now.Unix()/int64(etagBucket/time.Second), 10)))
	return `W/"` + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18]) + `"`
}

// canonicalQuery encodes q with sorted keys (url.Values.Encode sorts).
func canonicalQuery(q url.Values) string { return q.Encode() }

// etagMatches implements the weak comparison of If-None-Match.
func etagMatches(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == want {
			return true
		}
	}
	return false
}

// listNotModified is notModified for a list whose data is at version: the
// principal is validated first, so a 304 never goes to a caller the list
// itself would refuse.
func (a *api) listNotModified(w http.ResponseWriter, r *http.Request, p identity.Principal, version string) bool {
	if a.fleet.validPrincipal(p) != nil {
		return false // the handler answers the error
	}
	return a.notModified(w, r, listETag(p, r, version, time.Now()))
}

// notModified sets the validator headers of a list response and, when the
// request's If-None-Match matches, answers 304 and reports true.
func (a *api) notModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		a.metrics.notModified.Add(1)
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// writeJSONWithETag writes v with a validator derived from its own bytes
// (for lists without a cheap data version, such as threads), and answers
// 304 when If-None-Match matches it.
func (a *api) writeJSONWithETag(w http.ResponseWriter, r *http.Request, p identity.Principal, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	sum := sha256.Sum256(append([]byte(subjectKey(p)+"\x00"), b...))
	if a.notModified(w, r, `W/"`+base64.RawURLEncoding.EncodeToString(sum[:18])+`"`) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}
