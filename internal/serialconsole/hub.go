// Package serialconsole owns the live byte streams between QEMU serial
// devices and attached clients. It continuously drains each guest console,
// retains bounded output for a later attachment, appends all output to a
// per-VM log file, and permits one interactive client at a time.
package serialconsole

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var (
	ErrNotConnected = errors.New("the VM serial console is not ready")
	ErrInUse        = errors.New("the VM serial console is already in use")
)

const (
	defaultHistoryBytes = 1 << 20
	defaultDialTimeout  = 3 * time.Second
	retryInterval       = 10 * time.Millisecond
)

// Hub owns one upstream QEMU connection per running VM.
type Hub struct {
	mu           sync.Mutex
	socketPath   func(string) string
	logPath      func(string) string
	historyBytes int
	dialTimeout  time.Duration
	sessions     map[string]*session
}

// New returns a Hub that reaches each VM's QEMU serial socket at
// socketPath(id) and, when logPath is not nil, also appends its output to
// the file at logPath(id).
func New(socketPath, logPath func(string) string) *Hub {
	return &Hub{
		socketPath: socketPath, logPath: logPath, historyBytes: defaultHistoryBytes,
		dialTimeout: defaultDialTimeout, sessions: make(map[string]*session),
	}
}

// Start connects to the QEMU serial socket of a VM that has just been
// started and begins capturing output. The previous run's log is kept as
// LOG.1 and a new log is started. It is idempotent for a VM whose session
// is already running.
func (h *Hub) Start(id string) error {
	return h.start(id, true)
}

// Resume reconnects to a VM that kept running while App Runner restarted,
// appending to its existing log rather than starting a new one.
func (h *Hub) Resume(id string) error {
	return h.start(id, false)
}

func (h *Hub) start(id string, newRun bool) error {
	h.mu.Lock()
	if _, ok := h.sessions[id]; ok {
		h.mu.Unlock()
		return nil
	}
	s := &session{historyLimit: h.historyBytes, stop: make(chan struct{})}
	h.sessions[id] = s
	h.mu.Unlock()
	if h.logPath != nil {
		s.setLog(openLog(h.logPath(id), newRun))
	}

	connection, err := dialUntil(h.socketPath(id), h.dialTimeout, s.stop)
	if err != nil {
		s.close()
		h.mu.Lock()
		if h.sessions[id] == s {
			delete(h.sessions, id)
		}
		h.mu.Unlock()
		return err
	}
	if !s.setUpstream(connection) {
		_ = connection.Close()
		h.mu.Lock()
		if h.sessions[id] == s {
			delete(h.sessions, id)
		}
		h.mu.Unlock()
		return ErrNotConnected
	}
	go s.run(connection, h.socketPath(id))
	return nil
}

// Stop forgets captured output and closes the upstream, the attached client,
// and the log file.
func (h *Hub) Stop(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Close stops every console session without affecting its VM.
func (h *Hub) Close() {
	h.mu.Lock()
	sessions := make([]*session, 0, len(h.sessions))
	for id, s := range h.sessions {
		sessions = append(sessions, s)
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

// Attach returns the VM's exclusive interactive stream. Output captured since
// Start is delivered before live output.
func (h *Hub) Attach(id string) (io.ReadWriteCloser, error) {
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil {
		return nil, ErrNotConnected
	}
	return s.attach()
}

func dialUntil(path string, timeout time.Duration, stop <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrNotConnected
		}
		connection, err := net.DialTimeout("unix", path, min(100*time.Millisecond, remaining))
		if err == nil {
			return connection, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrNotConnected
		}
		select {
		case <-stop:
			return nil, ErrNotConnected
		case <-time.After(retryInterval):
		}
	}
}

type session struct {
	mu           sync.Mutex
	writeMu      sync.Mutex
	upstream     net.Conn
	history      []byte
	historyLimit int
	attached     *attachment
	log          *logFile
	stop         chan struct{}
	stopOnce     sync.Once
}

func (s *session) setUpstream(connection net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return false
	default:
	}
	s.upstream = connection
	return true
}

// setLog installs the session's log file unless the session has already
// been closed, in which case the file is closed instead.
func (s *session) setLog(log *logFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		log.Close()
	default:
		s.log = log
	}
}

func (s *session) run(connection net.Conn, socketPath string) {
	for {
		buffer := make([]byte, 32*1024)
		for {
			count, err := connection.Read(buffer)
			if count > 0 {
				s.log.Write(buffer[:count])
				s.publish(buffer[:count])
			}
			if err != nil {
				break
			}
		}
		_ = connection.Close()
		s.lostUpstream(connection)
		select {
		case <-s.stop:
			return
		default:
		}
		for {
			next, err := dialUntil(socketPath, defaultDialTimeout, s.stop)
			if err == nil {
				if !s.setUpstream(next) {
					_ = next.Close()
					return
				}
				connection = next
				break
			}
			select {
			case <-s.stop:
				return
			default:
			}
		}
	}
}

func (s *session) publish(contents []byte) {
	chunk := append([]byte(nil), contents...)
	s.mu.Lock()
	s.history = append(s.history, chunk...)
	if excess := len(s.history) - s.historyLimit; excess > 0 {
		copy(s.history, s.history[excess:])
		s.history = s.history[:s.historyLimit]
	}
	if s.attached != nil {
		select {
		case s.attached.output <- chunk:
		default:
			s.detachLocked(s.attached)
		}
	}
	s.mu.Unlock()
}

func (s *session) attach() (*Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upstream == nil {
		return nil, ErrNotConnected
	}
	if s.attached != nil {
		return nil, ErrInUse
	}
	attached := &attachment{output: make(chan []byte, 64)}
	if len(s.history) > 0 {
		attached.output <- append([]byte(nil), s.history...)
	}
	s.attached = attached
	return &Stream{session: s, attachment: attached}, nil
}

func (s *session) write(contents []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	connection := s.upstream
	s.mu.Unlock()
	if connection == nil {
		return 0, ErrNotConnected
	}
	return connection.Write(contents)
}

func (s *session) lostUpstream(connection net.Conn) {
	s.mu.Lock()
	if s.upstream == connection {
		s.upstream = nil
	}
	if s.attached != nil {
		s.detachLocked(s.attached)
	}
	s.mu.Unlock()
}

func (s *session) detach(attached *attachment) {
	s.mu.Lock()
	if s.attached == attached {
		s.detachLocked(attached)
	}
	s.mu.Unlock()
}

func (s *session) detachLocked(attached *attachment) {
	s.attached = nil
	attached.closeOnce.Do(func() { close(attached.output) })
}

func (s *session) close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	if s.upstream != nil {
		_ = s.upstream.Close()
		s.upstream = nil
	}
	if s.attached != nil {
		s.detachLocked(s.attached)
	}
	log := s.log
	s.mu.Unlock()
	log.Close()
}

type attachment struct {
	output    chan []byte
	closeOnce sync.Once
}

// Stream is one interactive attachment to a serial console.
type Stream struct {
	session    *session
	attachment *attachment
	current    []byte
	closed     bool
	mu         sync.Mutex
}

func (s *Stream) Read(target []byte) (int, error) {
	for len(s.current) == 0 {
		chunk, ok := <-s.attachment.output
		if !ok {
			return 0, io.EOF
		}
		s.current = chunk
	}
	count := copy(target, s.current)
	s.current = s.current[count:]
	return count, nil
}

func (s *Stream) Write(contents []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	return s.session.write(contents)
}

func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.session.detach(s.attachment)
	return nil
}
