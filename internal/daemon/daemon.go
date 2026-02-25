package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bduffany/kpf/internal/protocol"
	"github.com/bduffany/kpf/internal/tail"
)

const (
	reaperInterval = 1 * time.Minute
	stderrTailSize = 64
)

var forwardingLineRegexp = regexp.MustCompile(`Forwarding from .*:(\d+) ->`)

type daemon struct {
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	key string
	req protocol.Request
	ttl time.Duration

	mu         sync.Mutex
	cmd        *exec.Cmd
	readyErr   error
	local      int
	outputWait sync.WaitGroup
	stderrTail *tail.Buffer

	readyOnce sync.Once
	doneOnce  sync.Once
	readyCh   chan struct{}
	doneCh    chan struct{}

	onExit func(*session)

	lastActivityNanos atomic.Int64
}

// Run starts the kpf daemon server and serves requests over the unix socket.
func Run() error {
	sock := protocol.SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	if isLiveSocket(sock) {
		return nil
	}
	_ = os.Remove(sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) && isLiveSocket(sock) {
			return nil
		}
		return fmt.Errorf("listen on daemon socket: %w", err)
	}
	defer ln.Close()
	defer os.Remove(sock)
	_ = os.Chmod(sock, 0o600)

	d := &daemon{sessions: make(map[string]*session)}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go d.reaper(ctx)
	go func() {
		<-ctx.Done()
		d.stopAll()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				continue
			}
			return fmt.Errorf("accept: %w", err)
		}
		go d.handleConn(conn)
	}
}

func isLiveSocket(sock string) bool {
	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (d *daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(protocol.RequestTimeout))

	dec := json.NewDecoder(bufio.NewReader(conn))
	var req protocol.Request
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return
		}
		_ = json.NewEncoder(conn).Encode(protocol.Response{OK: false, Error: fmt.Sprintf("decode request: %s", err)})
		return
	}

	if req.Action == "" {
		req.Action = "ensure"
	}
	if req.Action != "ensure" {
		_ = json.NewEncoder(conn).Encode(protocol.Response{OK: false, Error: fmt.Sprintf("unknown action %q", req.Action)})
		return
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), protocol.RequestTimeout)
	defer cancel()

	localPort, err := d.ensureSession(waitCtx, req)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(protocol.Response{OK: false, Error: err.Error()})
		return
	}
	_ = json.NewEncoder(conn).Encode(protocol.Response{OK: true, LocalPort: localPort})
}

func (d *daemon) ensureSession(ctx context.Context, req protocol.Request) (int, error) {
	if req.Key == "" {
		return 0, errors.New("missing request key")
	}
	if len(req.Args) == 0 {
		return 0, errors.New("missing kubectl args")
	}

	s := d.getOrCreate(req)
	s.touch()
	return s.waitUntilReady(ctx)
}

func (d *daemon) getOrCreate(req protocol.Request) *session {
	d.mu.Lock()
	if s, ok := d.sessions[req.Key]; ok {
		if !s.done() {
			d.mu.Unlock()
			return s
		}
		delete(d.sessions, req.Key)
	}

	s := newSession(req, func(s *session) {
		d.mu.Lock()
		if cur, ok := d.sessions[s.key]; ok && cur == s {
			delete(d.sessions, s.key)
		}
		d.mu.Unlock()
	})
	d.sessions[req.Key] = s
	d.mu.Unlock()

	s.start()
	return s
}

func (d *daemon) reaper(ctx context.Context) {
	t := time.NewTicker(reaperInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.reapExpired(time.Now())
		}
	}
}

func (d *daemon) reapExpired(now time.Time) {
	d.mu.Lock()
	sessions := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()

	for _, s := range sessions {
		if now.Sub(s.lastActivity()) > s.ttl {
			s.stop()
		}
	}
}

func (d *daemon) stopAll() {
	d.mu.Lock()
	sessions := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()

	for _, s := range sessions {
		s.stop()
	}
}

func newSession(req protocol.Request, onExit func(*session)) *session {
	s := &session{
		key:        req.Key,
		req:        req,
		ttl:        sessionTTL(req.SessionTTLNanos),
		stderrTail: tail.NewBuffer(stderrTailSize),
		readyCh:    make(chan struct{}),
		doneCh:     make(chan struct{}),
		onExit:     onExit,
	}
	s.touch()
	return s
}

func sessionTTL(rawTTLNanos int64) time.Duration {
	if rawTTLNanos <= 0 {
		return protocol.DefaultSessionTTL
	}
	return time.Duration(rawTTLNanos)
}

func (s *session) start() {
	cmd := exec.Command("kubectl", append([]string{"port-forward"}, s.req.Args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.setReadyErr(fmt.Errorf("stdout pipe: %w", err))
		s.finish()
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.setReadyErr(fmt.Errorf("stderr pipe: %w", err))
		s.finish()
		return
	}

	if err := cmd.Start(); err != nil {
		s.setReadyErr(fmt.Errorf("start kubectl: %w", err))
		s.finish()
		return
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	s.outputWait.Add(2)
	go s.watchOutput(stdout, false)
	go s.watchOutput(stderr, true)

	go func() {
		err := cmd.Wait()
		s.outputWait.Wait()
		if err != nil {
			s.setReadyErr(s.kubectlWaitErr(err))
		}
		s.finish()
	}()
}

func (s *session) watchOutput(r io.Reader, stderr bool) {
	defer s.outputWait.Done()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if stderr {
			s.stderrTail.Add(line)
		}
		s.handleLogLine(line)
	}

	if stderr {
		if err := scanner.Err(); err != nil {
			s.stderrTail.Add(fmt.Sprintf("scanner error: %v", err))
		}
	}
}

func (s *session) kubectlWaitErr(err error) error {
	lines := s.stderrTail.Lines()
	if len(lines) == 0 {
		return fmt.Errorf("kubectl port-forward: %w", err)
	}
	return fmt.Errorf("kubectl port-forward: %w\nstderr tail:\n%s", err, strings.Join(lines, "\n"))
}

func (s *session) handleLogLine(line string) {
	if strings.Contains(line, "Handling connection for") {
		s.touch()
	}
	m := forwardingLineRegexp.FindStringSubmatch(line)
	if len(m) != 2 {
		return
	}
	p, err := strconv.Atoi(m[1])
	if err != nil || p <= 0 {
		return
	}
	s.mu.Lock()
	if s.local == 0 {
		s.local = p
	}
	s.mu.Unlock()
	s.touch()
	s.readyOnce.Do(func() { close(s.readyCh) })
}

func (s *session) touch() {
	s.lastActivityNanos.Store(time.Now().UnixNano())
}

func (s *session) lastActivity() time.Time {
	n := s.lastActivityNanos.Load()
	if n == 0 {
		return time.Unix(0, 0)
	}
	return time.Unix(0, n)
}

func (s *session) waitUntilReady(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.readyCh:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.readyErr != nil {
			return 0, s.readyErr
		}
		if s.local <= 0 {
			return 0, errors.New("port-forward started but local port is unknown")
		}
		return s.local, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, errors.New("timed out waiting for kubectl port-forward to start")
		}
		return 0, fmt.Errorf("waiting for kubectl port-forward to start: %w", ctx.Err())
	case <-s.doneCh:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.readyErr != nil {
			return 0, s.readyErr
		}
		return 0, errors.New("port-forward exited before becoming ready")
	}
}

func (s *session) setReadyErr(err error) {
	s.mu.Lock()
	if s.local == 0 && s.readyErr == nil {
		s.readyErr = err
	}
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.readyCh) })
}

func (s *session) stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *session) done() bool {
	select {
	case <-s.doneCh:
		return true
	default:
		return false
	}
}

func (s *session) finish() {
	s.doneOnce.Do(func() {
		close(s.doneCh)
		if s.onExit != nil {
			s.onExit(s)
		}
	})
}
