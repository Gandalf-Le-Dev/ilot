package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const apiDrainTimeout = 5 * time.Second

type Options struct {
	Paths   Paths
	Version string
	// The address of the reverse proxy, for example 127.0.0.1:4580. Empty: no proxy.
	ProxyListen string
	// With no logger, the daemon writes JSON records to the daemon.log of the state folder.
	Logger     *slog.Logger
	Supervisor SupervisorOptions
	// How long the processes of a dead daemon get to end after SIGTERM.
	OrphanGrace time.Duration
}

// Run is the whole life of the daemon. It returns ErrAlreadyRunning, and touches nothing, when another daemon lives.
func Run(ctx context.Context, options Options) error {
	paths := options.Paths
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return err
	}
	lock, err := acquireLock(paths.LockFile())
	if err != nil {
		return err
	}
	defer lock.Close()
	// The socket file goes last, right before the lock: a caller that sees no file can start the next daemon at once.
	defer os.Remove(paths.Socket())

	logger := options.Logger
	if logger == nil {
		logFile, err := openRotatingFile(paths.LogFile(), defaultLogMaxBytes)
		if err != nil {
			return err
		}
		defer logFile.Close()
		logger = slog.New(slog.NewJSONHandler(logFile, nil))
	}

	// Before the socket: a caller must not start an app while the old copy still holds its port.
	killOrphans(paths.RecordFile(), cmp.Or(options.OrphanGrace, defaultOrphanGrace), logger)

	routes := &RouteTable{}
	proxyAddress := ""
	if options.ProxyListen != "" {
		proxyListener, err := net.Listen("tcp", options.ProxyListen)
		if err != nil {
			logger.Error("the proxy cannot listen", "address", options.ProxyListen, "error", err)
			return fmt.Errorf("the proxy cannot listen on %s: %w", options.ProxyListen, err)
		}
		proxyAddress = proxyListener.Addr().String()
		proxyServer := &http.Server{Handler: NewProxy(routes, logger), ReadHeaderTimeout: 10 * time.Second}
		proxyDone := make(chan struct{})
		go func() {
			defer close(proxyDone)
			_ = proxyServer.Serve(proxyListener)
		}()
		defer func() {
			proxyServer.Close()
			<-proxyDone
		}()
	}

	listener, err := listenOnSocket(paths.Socket())
	if err != nil {
		logger.Error("daemon cannot listen", "error", err)
		return err
	}

	supervisorOptions := options.Supervisor
	supervisorOptions.Logger = logger
	supervisorOptions.RecordFile = paths.RecordFile()
	supervisor := NewSupervisor(supervisorOptions)
	shutdownRequested := make(chan struct{})
	api := &Server{
		Supervisor: supervisor,
		Routes:     routes,
		Status:     Status{Version: options.Version, PID: os.Getpid(), StartedAt: time.Now(), ProxyAddress: proxyAddress},
		OnShutdown: sync.OnceFunc(func() { close(shutdownRequested) }),
		Logger:     logger,
	}
	apiServer := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- apiServer.Serve(listener) }()
	logger.Info("daemon started", "version", options.Version, "pid", os.Getpid(), "socket", paths.Socket(), "proxy", proxyAddress)

	var failure error
	select {
	case <-ctx.Done():
	case <-shutdownRequested:
	case failure = <-served:
		served <- failure
	}

	supervisor.Shutdown()
	drain, cancel := context.WithTimeout(context.Background(), apiDrainTimeout)
	defer cancel()
	if err := apiServer.Shutdown(drain); err != nil {
		apiServer.Close()
	}
	<-served
	logger.Info("daemon stopped", "pid", os.Getpid())
	return failure
}

// listenOnSocket replaces a socket file. Only the holder of the lock may call it: the file of a live daemon is never stale.
func listenOnSocket(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
