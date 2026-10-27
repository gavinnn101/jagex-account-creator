package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	// maxBodyBytes bounds a request body, because the generated JSON decode reads with no limit.
	// A config with a pasted mailbox list holds a refresh token of about 1 KB for each mailbox,
	// so the bound leaves room for thousands of mailboxes.
	maxBodyBytes = 8 << 20
	// shutdownTimeout bounds the wait for the open requests after a stop.
	shutdownTimeout   = 5 * time.Second
	readHeaderTimeout = 5 * time.Second
	// apiWriteTimeout is above the 30 s bound of the mailbox check, so its answer reaches the client.
	apiWriteTimeout = time.Minute
	apiIdleTimeout  = 2 * time.Minute
)

// runServe reads the dashboard files from dashboard and opens the account store
// of serve. Then it serves the HTTP API and the dashboard of s on addr until
// ctx ends. It closes the store after serveAPI has waited for the run.
func runServe(ctx context.Context, stopNow <-chan struct{}, s *apiServer, addr string, dashboard func() (fs.FS, error)) error {
	// A missing dashboard is a build defect, so serve refuses before any side effect.
	spa, err := dashboard()
	if err != nil {
		return err
	}
	s.spa = spa
	// The store opens before the listen, so a store that fails to open never binds the port.
	accounts, err := openStore(ctx, s.logger, s.config())
	if err != nil {
		return err
	}
	s.store = accounts
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return errors.Join(fmt.Errorf("listening for the API: %w", err), accounts.Close())
	}
	s.logger.Info("serving the API", "addr", ln.Addr().String())
	return errors.Join(serveAPI(ctx, stopNow, s, ln, guardAPI(s.handler())), accounts.Close())
}

// serveAPI serves h on ln until ctx ends. Then it stops the run of s and keeps
// serving until the run ends, so a client sees the stop. A close of stopNow
// stops the run at once. Then it closes the streams, waits for the open
// requests for up to shutdownTimeout, or until stopNow closes, and closes the
// rest. A serve error also stops the run, waits for it and closes the streams.
func serveAPI(ctx context.Context, stopNow <-chan struct{}, s *apiServer, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      apiWriteTimeout,
		IdleTimeout:       apiIdleTimeout,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelDebug),
	}
	var serveErr error
	served := make(chan struct{})
	go func() {
		defer close(served)
		serveErr = srv.Serve(ln)
	}()

	select {
	case <-served:
		s.drainRun(stopNow)
		s.closeStreams()
		return fmt.Errorf("serving the API: %w", serveErr)
	case <-ctx.Done():
	}
	s.drainRun(stopNow)
	// Shutdown waits for each open stream, and a stream ends at the close of its hub.
	s.closeStreams()

	// ctx has ended, and Shutdown returns at once on an ended context, so the bound starts from a detached one.
	detached, cancelDetached := stopNowContext(ctx, stopNow)
	defer cancelDetached()
	bounded, cancel := context.WithTimeout(detached, shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(bounded); err != nil {
		s.logger.Warn("closing the API connections that are still open", "error", err)
	}
	// Shutdown has closed the listener, so Close has no listener error to return. It closes the connections that are still open.
	_ = srv.Close()
	// Serve returns http.ErrServerClosed after Shutdown, so its error carries nothing.
	<-served
	return nil
}

// guardAPI returns next behind the request checks, in this order: the Host
// check, the Sec-Fetch-Site check, http.CrossOriginProtection, the body bound,
// and the response headers that keep another origin from reading a response.
func guardAPI(next http.Handler) http.Handler {
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
	cop := http.NewCrossOriginProtection().Handler(bounded)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		// CrossOriginProtection allows every GET, so this check also keeps a cross-site page from a GET.
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		cop.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether the name of host, with or without a port, is
// localhost or an IP address. A DNS rebinding page always sends its own DNS name.
// Limitation: a LAN machine name fails, so a client on another machine uses the IP address.
// Limitation: a browser that resolves localhost over DNS would let a rebound name through. Chrome does not.
func hostAllowed(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		// A browser sends no port for the default port of the scheme.
		name = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if name == "localhost" {
		return true
	}
	_, err = netip.ParseAddr(name)
	return err == nil
}
