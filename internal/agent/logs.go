package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/protocol"
)

// Log streaming limits. A chunk is flushed every logFlushEvery, or sooner at
// logChunkLines lines or logChunkBytes bytes, which keeps every stream frame
// well under protocol.MaxFrameBytes.
const (
	maxTailLines     = 5000
	defaultTailLines = 1000
	maxLineBytes     = 8 << 10
	logChunkLines    = 100
	logChunkBytes    = 512 << 10
	logFlushEvery    = 200 * time.Millisecond
)

// streamLogs reads a Pod's logs through the impersonated client and hands
// them to stream in chunks. It returns when the log ends (or, with Follow,
// when ctx is cancelled).
func streamLogs(ctx context.Context, kube kubernetes.Interface, t model.Ref, args protocol.LogsArgs, stream func(protocol.LogChunk) error) error {
	tail := args.TailLines
	switch {
	case tail <= 0:
		tail = defaultTailLines
	case tail > maxTailLines:
		tail = maxTailLines
	}
	opts := &corev1.PodLogOptions{Container: args.Container, Follow: args.Follow, TailLines: &tail}
	rc, err := kube.CoreV1().Pods(t.Namespace).GetLogs(t.Name, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("agent: logs of %s: %w", t.ID(), err)
	}
	defer rc.Close()
	return pumpLines(ctx, rc, stream)
}

func pumpLines(ctx context.Context, r io.Reader, stream func(protocol.LogChunk) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan string, logChunkLines)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		br := bufio.NewReaderSize(r, 64<<10)
		for {
			line, err := readLine(br)
			if err == nil || (errors.Is(err, io.EOF) && line != "") {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr <- err
				}
				return
			}
		}
	}()

	ticker := time.NewTicker(logFlushEvery)
	defer ticker.Stop()
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := stream(protocol.LogChunk{Lines: batch})
		batch, size = nil, 0
		return err
	}
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if err := flush(); err != nil {
					return err
				}
				select {
				case err := <-readErr:
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("agent: read logs: %w", err)
				default:
					return nil
				}
			}
			batch = append(batch, line)
			size += len(line)
			if len(batch) >= logChunkLines || size >= logChunkBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readLine returns the next line without its newline, truncated to
// maxLineBytes. The rest of an over-long line is discarded.
func readLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		if room := maxLineBytes - len(buf); room > 0 {
			buf = append(buf, frag[:min(len(frag), room)]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		n := len(buf)
		if n > 0 && buf[n-1] == '\n' {
			buf = buf[:n-1]
		}
		return string(buf), err
	}
}
