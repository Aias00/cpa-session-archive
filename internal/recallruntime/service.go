// Package recallruntime owns the HTTP and projection lifecycle of local recall.
package recallruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/memory"
)

type Config struct {
	Listen, TokenFile, CredentialsFile string
	// ReservedToken must never grant recall access, including scoped credentials.
	ReservedToken  string
	ReservedTokens []string
	Diagnostics    func(context.Context) (ingeststatus.Status, error)
}

type Service struct {
	projection memory.Projection
	listener   net.Listener
	server     *http.Server
	cancel     context.CancelFunc
	workerDone chan struct{}
	serveDone  chan struct{}
	done       chan struct{}
	once       sync.Once
	errMu      sync.Mutex
	err        error
	requestMu  sync.Mutex
	closing    bool
	requests   sync.WaitGroup
}

// Start takes ownership of projection only on success. It never contacts the
// source feed during setup, allowing an independent collector to restart later.
func Start(parent context.Context, projection memory.Projection, cfg Config) (*Service, error) {
	if projection == nil {
		return nil, fmt.Errorf("memory projection is required")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("invalid memory listen address")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("memory listener must bind a loopback address")
		}
	}
	credentials, err := loadCredentials(cfg)
	if err != nil {
		return nil, err
	}
	reader := memory.WithIngestionStatusContext(projection, cfg.Diagnostics)
	handler, err := memory.NewHandlerWithCredentials(reader, credentials)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("memory resolved outside loopback")
	}
	if err := parent.Err(); err != nil {
		listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{projection: projection, listener: listener, cancel: cancel, workerDone: make(chan struct{}), serveDone: make(chan struct{}), done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		status, err := projection.Status(r.Context())
		if err != nil || !status.BackfillDone || !status.MessageMetadataDone {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	s.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestMu.Lock()
		if s.closing {
			s.requestMu.Unlock()
			http.Error(w, "recall stopping", http.StatusServiceUnavailable)
			return
		}
		s.requests.Add(1)
		s.requestMu.Unlock()
		defer s.requests.Done()
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		projection.Run(ctx)
		stopped := ctx.Err() == nil
		if stopped {
			s.recordError(fmt.Errorf("memory projection worker stopped"))
		}
		close(s.workerDone)
		if stopped {
			go s.Close()
		}
	}()
	go func() {
		err := s.server.Serve(listener)
		stopped := err != nil && !errors.Is(err, http.ErrServerClosed)
		if stopped {
			s.recordError(fmt.Errorf("memory HTTP listener stopped: %w", err))
		}
		close(s.serveDone)
		if stopped {
			go s.Close()
		}
	}()
	go func() {
		select {
		case <-parent.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *Service) Addr() net.Addr        { return s.listener.Addr() }
func (s *Service) Done() <-chan struct{} { return s.done }

func (s *Service) recordError(err error) {
	s.errMu.Lock()
	s.err = errors.Join(s.err, err)
	s.errMu.Unlock()
}

func (s *Service) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close cancels source requests and the worker, drains active handlers, and
// closes projection only after its HTTP readers and writer have exited.
func (s *Service) Close() error {
	s.once.Do(func() {
		s.requestMu.Lock()
		s.closing = true
		s.requestMu.Unlock()
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			s.recordError(err)
			_ = s.server.Close()
		}
		<-s.serveDone
		<-s.workerDone
		s.requests.Wait()
		if err := s.projection.Close(); err != nil {
			s.recordError(err)
		}
		close(s.done)
	})
	return s.Err()
}
