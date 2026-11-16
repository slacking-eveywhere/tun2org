// Package app orchestrates a full ntun run: TUN device + routes + gVisor
// netstack + tunnel handler + reconnecting SSH relay. It is the entry point
// behind `ntun up`.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"ntun-ng/internal/config"
	"ntun-ng/internal/dns"
	"ntun-ng/internal/mux"
	"ntun-ng/internal/netstack"
	"ntun-ng/internal/relay"
	"ntun-ng/internal/route"
	"ntun-ng/internal/state"
	"ntun-ng/internal/supervise"
	"ntun-ng/internal/tun"
	"ntun-ng/internal/tunnel"
)

// Logger is the minimal logging surface.
type Logger interface {
	Printf(format string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Run brings the tunnel up and blocks until ctx is cancelled or a fatal error
// occurs. It performs graceful teardown (routes, TUN, post-stop script) on
// exit.
func Run(ctx context.Context, cfg *config.Config, log Logger) error {
	if log == nil {
		log = discardLogger{}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 1. TUN device.
	dev, err := tun.Open(cfg.TunDevice, cfg.MTU)
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}
	defer dev.Close()
	log.Printf("app: tun %s up (mtu %d)", dev.Name(), dev.MTU())

	if err := dev.Configure(cfg.TunNetwork, cfg.TunNetworkV6, cfg.MTU); err != nil {
		return fmt.Errorf("app: %w", err)
	}

	// 2. Routes.
	routes := route.NewManager(dev.Name())
	added, err := routes.Add(cfg.Routes)
	if err != nil {
		// Continue: partial routes still let some traffic through. Log + proceed.
		log.Printf("app: route add: %v", err)
	}
	defer func() { _ = routes.Remove() }()
	for _, r := range added {
		log.Printf("app: route %s via %s", r.CIDR, r.Device)
	}

	// 2b. State file for `ntun down` crash recovery.
	routeCIDRs := make([]string, 0, len(added))
	for _, r := range added {
		routeCIDRs = append(routeCIDRs, r.CIDR)
	}
	if _, err := state.Write(state.File{
		Version:        1,
		PID:            os.Getpid(),
		StartedAt:      time.Now(),
		Device:         dev.Name(),
		Routes:         routeCIDRs,
		PostStopScript: cfg.PostStopScript,
	}); err != nil {
		log.Printf("app: state write: %v", err)
	}
	defer state.Remove()

	// 3. Post-start script.
	if cfg.PostStartScript != "" {
		if err := runScript(cfg.PostStartScript); err != nil {
			log.Printf("app: post_start: %v", err)
		}
	}
	defer func() {
		if cfg.PostStopScript != "" {
			if err := runScript(cfg.PostStopScript); err != nil {
				log.Printf("app: post_stop: %v", err)
			}
		}
	}()

	// 4. Relay (SSH transport) + Tunnel (netstack<->mux bridge).
	sshDialer, err := relay.NewDialer(cfg)
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}
	r := relay.New(relay.Options{
		Dialer:       sshDialer,
		PingEvery:    cfg.PingEvery,
		ReconnectMin: cfg.ReconnectMin,
		ReconnectMax: cfg.ReconnectMax,
		Log:          logAdapter{log: log, prefix: "relay"},
	})
	tnl := tunnel.New(tunnel.Options{
		Dialer:    &relayDialer{r: r},
		Log:       logAdapter{log: log, prefix: "tunnel"},
		DNSHijack: cfg.DNSHijack != nil && *cfg.DNSHijack,
		DNSRemote: cfg.DNSRemote,
	})

	// 5. gVisor netstack.
	link, err := netstack.NewLinkEndpoint(dev, uint32(dev.MTU()), dev.Offset())
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}
	stk, err := netstack.CreateStack(netstack.Config{
		Link:    link,
		Handler: tnl,
	})
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}
	tnl.AttachStack(stk)
	// Teardown order matters. stack.Wait() blocks until LinkEndpoint.Wait()
	// returns, and the link dispatch loop blocks in dev.Read() until the TUN
	// is closed. So stop the link loops and close the TUN *before* closing the
	// stack (this defer is registered last, so it runs first).
	defer func() {
		cancel()
		link.Close()
		_ = dev.Close()
		_ = tnl.Close()
	}()

	// If the link loops die unexpectedly (e.g. the TUN fd errors — the stack
	// then goes deaf while routes and the relay session stay up, which looks
	// exactly like a wedged tunnel), tear the whole tunnel down instead of
	// running as a zombie.
	go func() {
		link.Wait()
		if ctx.Err() == nil {
			log.Printf("app: netstack link loops exited unexpectedly — shutting the tunnel down")
			cancel()
		}
	}()

	// 5b. Interface watcher: rebind the SSH socket when the default route
	// changes (ethernet → wifi → vpn), in-process (mirrors legacy tun2socks
	// child-restart hack).
	rebind := &supervise.RebindTracker{}
	if cfg.Interface != "" {
		rebind.Set(cfg.Interface) // pin to configured interface
	}
	watcher := supervise.NewPollWatcher(5 * time.Second)
	go func() {
		_ = watcher.Watch(ctx, func(iface string) {
			if cfg.Interface != "" {
				return // explicit interface override; don't auto-rebind
			}
			if rebind.Set(iface) {
				log.Printf("app: default interface changed to %s, rebinding", iface)
				r.Rebind()
			}
		})
	}()

	// 5c. Optional local DNS server (dns_listen) forwarding over the mux.
	if cfg.DNSListen != "" && cfg.DNSRemote != "" {
		srv := dns.NewListenServer(cfg.DNSListen, cfg.DNSRemote, &relayDialer{r: r}, 0,
			logAdapter{log: log, prefix: "dns"})
		go func() {
			if err := srv.ListenAndServe(ctx); err != nil {
				log.Printf("app: dns server: %v", err)
			}
		}()
	}

	// 6. Run relay loop (reconnects). Blocks until ctx cancelled.
	relayDone := make(chan struct{})
	var relayErr error
	go func() {
		defer close(relayDone)
		relayErr = r.Run(ctx)
	}()

	log.Printf("app: tunnel up")
	select {
	case <-ctx.Done():
	case <-relayDone:
		if relayErr != nil {
			return fmt.Errorf("app: relay: %w", relayErr)
		}
	}
	// Wait for the reconnect loop to stop before tearing down. Bounded so a
	// wedged SSH handshake (which does not take a context) can't block exit;
	// the process exits right after this returns anyway.
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
	}
	log.Printf("app: shutting down")
	return nil
}

// runScript runs a shell script fragment via `sh -c`.
func runScript(s string) error {
	cmd := exec.Command("sh", "-c", s)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// relayDialer adapts *relay.Relay to tunnel.Dialer and exposes the relay so
// app.Run can drive its reconnect loop.
type relayDialer struct {
	r *relay.Relay
}

func (d *relayDialer) Open(a mux.Addr) (*mux.Conn, error)       { return d.r.Open(a) }
func (d *relayDialer) OpenUDP(a mux.Addr) (*mux.UDPConn, error) { return d.r.OpenUDP(a) }

// logAdapter prefixes log lines.
type logAdapter struct {
	log    Logger
	prefix string
}

func (l logAdapter) Printf(format string, args ...any) {
	l.log.Printf(l.prefix+": "+format, args...)
}
