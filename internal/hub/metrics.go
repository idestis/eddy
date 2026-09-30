package hub

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/eddy-gitops/eddy/internal/protocol"
	"github.com/eddy-gitops/eddy/internal/version"
)

// metrics is a tiny Prometheus text-format exposition. The hub exposes a
// handful of counters and gauges, which does not justify a client library.
// Every label has a small, fixed value set.
type metrics struct {
	agentsConnected atomic.Int64
	sseClients      atomic.Int64
	logStreams      atomic.Int64

	agentAuthFailures atomic.Uint64
	framesThrottled   atomic.Uint64
	framesInvalid     atomic.Uint64
	sarHits           atomic.Uint64
	sarMisses         atomic.Uint64
	sseDropped        atomic.Uint64

	mu     sync.Mutex
	http   map[int]uint64
	frames map[protocol.FrameType]uint64
}

func newMetrics() *metrics {
	return &metrics{http: map[int]uint64{}, frames: map[protocol.FrameType]uint64{}}
}

func (m *metrics) request(code int) {
	m.mu.Lock()
	m.http[code]++
	m.mu.Unlock()
}

var knownFrames = []protocol.FrameType{
	protocol.TypeHello, protocol.TypeSnapshot, protocol.TypeDelta, protocol.TypeResponse,
	protocol.TypeStream, protocol.TypeStreamEnd, protocol.TypePing,
}

func (m *metrics) frame(t protocol.FrameType) {
	if !slices.Contains(knownFrames, t) {
		t = "other"
	}
	m.mu.Lock()
	m.frames[t]++
	m.mu.Unlock()
}

func (m *metrics) write(w io.Writer) {
	gauge := func(name, help string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
	}
	counter := func(name, help string, v uint64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	fmt.Fprintf(w, "# HELP eddy_hub_build_info Build information.\n# TYPE eddy_hub_build_info gauge\neddy_hub_build_info{version=%s} 1\n",
		strconv.Quote(version.Version))
	gauge("eddy_hub_agents_connected", "Agents with an open session.", m.agentsConnected.Load())
	gauge("eddy_hub_sse_clients", "Open /api/v1/stream connections.", m.sseClients.Load())
	gauge("eddy_hub_log_streams", "Open pod log streams.", m.logStreams.Load())
	counter("eddy_hub_agent_auth_failures_total", "Rejected agent connection attempts.", m.agentAuthFailures.Load())
	counter("eddy_hub_agent_frames_throttled_total", "Agent frames delayed by the per-agent rate limit.", m.framesThrottled.Load())
	counter("eddy_hub_agent_frames_invalid_total", "Agent frames that could not be decoded.", m.framesInvalid.Load())
	counter("eddy_hub_sar_cache_hits_total", "Access checks answered from the cache.", m.sarHits.Load())
	counter("eddy_hub_sar_cache_misses_total", "Access checks sent to an agent.", m.sarMisses.Load())
	counter("eddy_hub_sse_overflows_total", "SSE clients that fell behind and were resynced.", m.sseDropped.Load())

	m.mu.Lock()
	httpCounts := maps.Clone(m.http)
	frames := maps.Clone(m.frames)
	m.mu.Unlock()

	fmt.Fprint(w, "# HELP eddy_hub_http_requests_total HTTP requests on the UI listener by status code.\n# TYPE eddy_hub_http_requests_total counter\n")
	for _, code := range slices.Sorted(maps.Keys(httpCounts)) {
		fmt.Fprintf(w, "eddy_hub_http_requests_total{code=\"%d\"} %d\n", code, httpCounts[code])
	}
	fmt.Fprint(w, "# HELP eddy_hub_agent_frames_total Frames received from agents by type.\n# TYPE eddy_hub_agent_frames_total counter\n")
	for _, t := range slices.Sorted(maps.Keys(frames)) {
		fmt.Fprintf(w, "eddy_hub_agent_frames_total{type=%s} %d\n", strconv.Quote(string(t)), frames[t])
	}
}

func (m *metrics) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.write(w)
	})
}
