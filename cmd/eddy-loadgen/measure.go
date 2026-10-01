//go:build dev

package main

import (
	"fmt"
	"net"
	"runtime"
	"runtime/metrics"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// countingListener counts the bytes read from and written to every
// connection it accepts.
type countingListener struct {
	net.Listener
	read, written, accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.accepted.Add(1)
	return &countingConn{Conn: c, l: l}, nil
}

type countingConn struct {
	net.Conn
	l *countingListener
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.l.read.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.l.written.Add(int64(n))
	return n, err
}

// liveHeap forces two collections and returns the live heap in bytes.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// cpuTime is the process's user plus system CPU time.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// samples collects request measurements for one endpoint.
type samples struct {
	mu      sync.Mutex
	lat     []time.Duration
	wire    []int64
	decoded []int64
	status  map[int]int
}

func (s *samples) add(d time.Duration, wire, decoded int64, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lat = append(s.lat, d)
	s.wire = append(s.wire, wire)
	s.decoded = append(s.decoded, decoded)
	if s.status == nil {
		s.status = map[int]int{}
	}
	s.status[status]++
}

type summary struct {
	n                  int
	p50, p95, max      time.Duration
	wireAvg, decodeAvg int64
	statuses           map[int]int
}

func (s *samples) summary() summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := summary{n: len(s.lat), statuses: s.status}
	if out.n == 0 {
		return out
	}
	lat := slices.Clone(s.lat)
	slices.Sort(lat)
	out.p50 = lat[len(lat)/2]
	out.p95 = lat[min(len(lat)-1, len(lat)*95/100)]
	out.max = lat[len(lat)-1]
	var w, d int64
	for i := range s.wire {
		w += s.wire[i]
		d += s.decoded[i]
	}
	out.wireAvg, out.decodeAvg = w/int64(out.n), d/int64(out.n)
	return out
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
}

func bytesStr(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
