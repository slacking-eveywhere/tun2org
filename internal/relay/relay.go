// Package relay owns the SSH transport between the local ntun client and the
// remote ntun-agent. It handles:
//
//   - SSH authentication (password via keyboard-interactive, or ssh-agent),
//   - direct vs bastion connection modes,
//   - uploading + caching the ntun-agent binary on the remote host,
//   - exec'ing the agent over an SSH session and exposing its stdin/stdout as
//     an io.ReadWriteCloser (the mux transport),
//   - reconnect with exponential backoff + jitter,
//   - a live *mux.Session accessor (Open) that survives session swaps.
//
// The SSH machinery lives behind the Dialer interface so the reconnect logic
// is unit-testable without a real SSH server.
package relay

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"ntun-ng/internal/mux"
)

// Dialer opens one transport to the remote agent. Implementations:
//   - *sshDialer (real, internal/relay/ssh.go): SSH dial + upload + exec agent.
//   - fakes in tests.
//
// The returned io.ReadWriteCloser is the agent's stdin/stdout; Close must tear
// down the underlying SSH session + client. A non-nil error means no transport
// was established; retry with backoff.
type Dialer interface {
	DialSession(ctx context.Context) (io.ReadWriteCloser, error)
}

// Logger is the minimal logging surface used by the relay.
type Logger interface {
	Printf(format string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Options tunes the Relay reconnect loop.
type Options struct {
	Dialer       Dialer
	PingEvery    time.Duration
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	Log          Logger
}

// Relay maintains a live *mux.Session over a reconnecting SSH transport and
// exposes Open for the local side to dial targets through the agent.
type Relay struct {
	opts Options

	cur atomic.Pointer[mux.Session]

	// readyCond broadcasts whenever the live session pointer changes, so
	// waiters in Open/OpenUDP can reliably wake and re-check cur (see Open).
	readyMu   sync.Mutex
	readyCond *sync.Cond

	rebindCh chan struct{} // Rebind() sends here; Run drops the current session

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Relay. Call Run to start the reconnect loop.
func New(opts Options) *Relay {
	if opts.ReconnectMin <= 0 {
		opts.ReconnectMin = time.Second
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = 30 * time.Second
	}
	if opts.Log == nil {
		opts.Log = discardLogger{}
	}
	r := &Relay{
		opts:     opts,
		rebindCh: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	r.readyCond = sync.NewCond(&r.readyMu)
	return r
}

// Run drives the reconnect loop until ctx is cancelled. It returns when ctx
// is done. The first dial attempt is not delayed; subsequent attempts back
// off exponentially.
func (r *Relay) Run(ctx context.Context) error {
	ctx, r.cancel = context.WithCancel(ctx)
	defer close(r.done)
	defer r.cancel()

	var attempt int
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rwc, err := r.opts.Dialer.DialSession(ctx)
		if err != nil {
			r.opts.Log.Printf("relay: dial failed: %v", err)
			r.backoff(ctx, attempt)
			attempt++
			continue
		}
		attempt = 0
		r.opts.Log.Printf("relay: session established")

		sess := mux.NewSession(rwc, mux.SessionConfig{
			AcceptFunc: nil, // local side never accepts
			PingEvery:  r.opts.PingEvery,
		})
		r.publishSession(sess)

		go sess.Serve()
		// Wait for the session to end, a rebind request, or shutdown.
		select {
		case <-sess.Done():
		case <-r.rebindCh:
			r.opts.Log.Printf("relay: rebind requested, dropping session")
			_ = rwc.Close()
			<-sess.Done()
		case <-ctx.Done():
			_ = rwc.Close()
			<-sess.Done()
			return ctx.Err()
		}
		if err := sess.Err(); err != nil {
			r.opts.Log.Printf("relay: session ended: %v", err)
		}
		r.publishSession(nil)

		if err := ctx.Err(); err != nil {
			return err
		}
		// Brief pause before redialing to avoid hot-looping a responsive-but-
		// immediately-dying peer.
		r.backoff(ctx, 0)
	}
}

// Rebind requests that the relay drop its current session and re-establish a
// new one (e.g. after the default network interface changed). It is async and
// non-blocking; a second call before the rebind completes is coalesced.
func (r *Relay) Rebind() {
	select {
	case r.rebindCh <- struct{}{}:
	default:
	}
}

func (r *Relay) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

// Open dials a target through the currently live session. It blocks until a
// session is available (waiting through reconnects). Returns an error if the
// relay is stopped or the session dies mid-handshake.
func (r *Relay) Open(addr mux.Addr) (*mux.Conn, error) {
	for {
		sess := r.cur.Load()
		if sess != nil {
			c, err := sess.Open(addr)
			if err == nil {
				return c, nil
			}
			if !errors.Is(err, mux.ErrSessionClosed) {
				return nil, err
			}
			// session died; wait for the next one
		}
		if err := r.waitForSession(); err != nil {
			return nil, err
		}
	}
}

// OpenUDP opens a UDP association through the currently live session. Same
// reconnect semantics as Open.
func (r *Relay) OpenUDP(addr mux.Addr) (*mux.UDPConn, error) {
	for {
		sess := r.cur.Load()
		if sess != nil {
			c, err := sess.OpenUDP(addr)
			if err == nil {
				return c, nil
			}
			if !errors.Is(err, mux.ErrSessionClosed) {
				return nil, err
			}
		}
		if err := r.waitForSession(); err != nil {
			return nil, err
		}
	}
}

// waitForSession blocks until a (possibly nil) session is published or the
// relay is stopped. It re-checks cur under the condition lock to avoid missing
// a session that goes live between the check and the wait — the race that can
// otherwise leave Open parked forever on a stale notification.
func (r *Relay) waitForSession() error {
	r.readyMu.Lock()
	defer r.readyMu.Unlock()
	for {
		if r.cur.Load() != nil {
			return nil
		}
		select {
		case <-r.done:
			return ErrRelayStopped
		default:
		}
		r.readyCond.Wait()
	}
}

// publishSession stores the live session and wakes anyone waiting on it. A nil
// session means the previous one died; waiters wake, re-check, and block again
// until the next live session.
func (r *Relay) publishSession(sess *mux.Session) {
	r.readyMu.Lock()
	r.cur.Store(sess)
	r.readyCond.Broadcast()
	r.readyMu.Unlock()
}

// ErrRelayStopped is returned by Open when the relay has been stopped.
var ErrRelayStopped = errors.New("relay: stopped")

func (r *Relay) backoff(ctx context.Context, attempt int) {
	d := r.opts.ReconnectMin << uint(attempt)
	if d > r.opts.ReconnectMax {
		d = r.opts.ReconnectMax
	}
	// Full jitter.
	if d > 0 {
		d = time.Duration(rand.Int64N(int64(d)))
	}
	if d < r.opts.ReconnectMin {
		d = r.opts.ReconnectMin
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
