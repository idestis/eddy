package postgres

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/inproc"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// EventsChannel is the LISTEN/NOTIFY channel for store.Events.
const EventsChannel = "eddy_events"

// Listener reconnect backoff.
const (
	listenConnectTimeout = 10 * time.Second
	listenBackoffMin     = 100 * time.Millisecond
	listenBackoffMax     = 5 * time.Second
)

type events struct{ s *Store }

// Publish sends e with pg_notify through the pool. PostgreSQL delivers it
// to every listening session, including this process's own listener.
func (x events) Publish(ctx context.Context, e store.Event) error {
	b, err := storeutil.EncodeEvent(e)
	if err != nil {
		return err
	}
	_, err = x.s.db.ExecContext(ctx, `SELECT pg_notify($1, $2)`, EventsChannel, string(b))
	return mapErr("publish event", err)
}

func (x events) Subscribe(ctx context.Context) (<-chan store.Event, error) {
	return x.s.listen.subscribe(ctx)
}

// listener owns the one dedicated connection that LISTENs on EventsChannel.
// A LISTEN is tied to a session, so it cannot use the database/sql pool,
// which hands connections out per statement. The listener starts on the
// first Subscribe and runs until Close. When the connection drops it
// reconnects with backoff, LISTENs again and sends store.EventResync to
// every subscriber, because notifications sent in between were lost.
type listener struct {
	dsn    string
	log    *slog.Logger
	broker *inproc.Broker

	mu      sync.Mutex
	started bool
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	pid atomic.Uint32 // backend pid of the current connection; 0 while down
}

func newListener(dsn string, log *slog.Logger) *listener {
	ctx, cancel := context.WithCancel(context.Background())
	return &listener{dsn: dsn, log: log, broker: inproc.NewBroker(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// subscribe starts the listener if needed. The first call connects
// synchronously and fails if it cannot LISTEN, so a subscriber never
// believes it is listening when it is not.
func (l *listener) subscribe(ctx context.Context) (<-chan store.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, inproc.ErrClosed
	}
	if !l.started {
		conn, err := l.connect(ctx)
		if err != nil {
			return nil, err
		}
		l.started = true
		go l.run(conn)
	}
	return l.broker.Subscribe(ctx)
}

func (l *listener) connect(ctx context.Context) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(l.dsn)
	if err != nil {
		return nil, errors.New("postgres: listen: invalid DSN")
	}
	ctx, cancel := context.WithTimeout(ctx, listenConnectTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, mapErr("listen: connect", err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+EventsChannel); err != nil {
		closeConn(conn)
		return nil, mapErr("listen", err)
	}
	l.pid.Store(conn.PgConn().PID())
	return conn, nil
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func (l *listener) run(conn *pgx.Conn) {
	defer close(l.done)
	for {
		n, err := conn.WaitForNotification(l.ctx)
		if err == nil {
			e, err := storeutil.DecodeEvent([]byte(n.Payload))
			if err != nil {
				l.log.Warn("dropping malformed store event", "error", err)
				continue
			}
			l.broker.Broadcast(e)
			continue
		}
		l.pid.Store(0)
		closeConn(conn)
		if l.ctx.Err() != nil {
			return
		}
		l.log.Warn("store event listener disconnected; reconnecting", "error", err)
		if conn = l.reconnect(); conn == nil {
			return
		}
		l.log.Info("store event listener reconnected")
		l.broker.Broadcast(store.Event{Kind: store.EventResync})
	}
}

// reconnect retries connect with exponential backoff until it succeeds or
// the listener closes (then it returns nil).
func (l *listener) reconnect() *pgx.Conn {
	backoff := listenBackoffMin
	for {
		t := time.NewTimer(backoff)
		select {
		case <-l.ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		conn, err := l.connect(l.ctx)
		if err == nil {
			return conn
		}
		if l.ctx.Err() != nil {
			return nil
		}
		l.log.Warn("store event listener reconnect failed", "error", err, "retryIn", backoff.String())
		backoff = min(backoff*2, listenBackoffMax)
	}
}

// close stops the listener and closes every subscription. It is idempotent.
func (l *listener) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	started := l.started
	l.mu.Unlock()
	l.cancel()
	if started {
		<-l.done
	}
	l.broker.Close()
}
