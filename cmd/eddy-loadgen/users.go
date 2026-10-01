//go:build dev

package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/loadgen/synth"
)

// browser is one simulated user's HTTP client. It authenticates through
// proxy headers (the hub trusts 127.0.0.1 with the shared secret) and
// keeps the session cookie the hub mints, like a browser behind an
// auth proxy.
type browser struct {
	user   synth.User
	base   string
	secret string
	http   *http.Client

	mu    sync.Mutex
	etags map[string]string
}

func newBrowser(u synth.User, base, secret string, tr http.RoundTripper) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{user: u, base: base, secret: secret, etags: map[string]string{},
		http: &http.Client{Transport: tr, Jar: jar, Timeout: 5 * time.Minute}}
}

func (b *browser) newRequest(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Forwarded-Email", b.user.Name)
	req.Header.Set("X-Forwarded-Groups", strings.Join(strings.Fields(strings.ReplaceAll(strings.Join(b.user.Groups, " "), "eddy:", "")), ","))
	req.Header.Set("X-Eddy-Proxy-Secret", b.secret)
	req.Header.Set("Accept-Encoding", "gzip")
	return req, nil
}

// result is one measured request.
type result struct {
	status  int
	wire    int64
	decoded int64
	dur     time.Duration
	body    []byte // decoded, only when keep is set
	etag    string
}

// get fetches path and measures it. With revalidate, a previously seen
// ETag is sent as If-None-Match.
func (b *browser) get(ctx context.Context, path string, keep, revalidate bool) (result, error) {
	req, err := b.newRequest(ctx, path)
	if err != nil {
		return result{}, err
	}
	if revalidate {
		b.mu.Lock()
		et := b.etags[path]
		b.mu.Unlock()
		if et != "" {
			req.Header.Set("If-None-Match", et)
		}
	}
	start := time.Now()
	resp, err := b.http.Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	cr := &countReader{r: resp.Body}
	var body io.Reader = cr
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(cr)
		if err != nil {
			return result{}, fmt.Errorf("gzip %s: %w", path, err)
		}
		body = zr
	}
	var decoded int64
	var kept []byte
	if keep {
		kept, err = io.ReadAll(body)
		decoded = int64(len(kept))
	} else {
		decoded, err = io.Copy(io.Discard, body)
	}
	if err != nil {
		return result{}, fmt.Errorf("read %s: %w", path, err)
	}
	r := result{status: resp.StatusCode, wire: cr.n, decoded: decoded, dur: time.Since(start), body: kept, etag: resp.Header.Get("ETag")}
	if r.etag != "" {
		b.mu.Lock()
		b.etags[path] = r.etag
		b.mu.Unlock()
	}
	return r, nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// stream holds one SSE connection open and counts what it receives.
type stream struct {
	wire, events atomic.Int64
	changes      atomic.Int64
	clusters     atomic.Int64
	done         chan struct{}
}

func streamsWithClusters(ss []*stream) int {
	n := 0
	for _, s := range ss {
		if s.clusters.Load() > 0 {
			n++
		}
	}
	return n
}

func (b *browser) openStream(ctx context.Context) (*stream, error) {
	req, err := b.newRequest(ctx, "/api/v1/stream")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Transport: b.http.Transport, Jar: b.http.Jar}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream: status %d", resp.StatusCode)
	}
	s := &stream{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer resp.Body.Close()
		cr := &atomicCountReader{r: resp.Body, n: &s.wire}
		var body io.Reader = cr
		if resp.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(cr)
			if err != nil {
				return
			}
			body = zr
		}
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			line := sc.Text()
			if ev, ok := strings.CutPrefix(line, "event: "); ok {
				s.events.Add(1)
				switch ev {
				case "change":
					s.changes.Add(1)
				case "clusters":
					s.clusters.Add(1)
				}
			}
		}
	}()
	return s, nil
}

type atomicCountReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *atomicCountReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// clusterCounts is the subset of ClusterInfo the loadgen reads.
type clusterCounts struct {
	Name      string         `json:"name"`
	Connected bool           `json:"connected"`
	Counts    map[string]int `json:"counts"`
}

func decodeClusters(b []byte) ([]clusterCounts, error) {
	var out struct {
		Items []clusterCounts `json:"items"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
