package netstack

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// loopRW is an io.ReadWriter that acts as a TUN loopback: every packet the
// stack writes (outboundLoop) is queued and served back through Read, so the
// dispatchLoop consumes it via InjectInbound — the exact production data path
// (kernel <-> ntun), minus the OS kernel.
//
// Close unblocks any pending Read (with an error) so teardown of the dispatch
// loop never deadlocks stack.Destroy.
type loopRW struct {
	mu     sync.Mutex
	ready  chan struct{}
	q      [][]byte
	closed bool
	nw     int      // total bytes written (outbound)
	track  [][]byte // captured outbound frames when non-nil
}

func (r *loopRW) readLocked() ([]byte, bool) {
	if len(r.q) > 0 {
		next := r.q[0]
		r.q = r.q[1:]
		return next, true
	}
	return nil, false
}

func (r *loopRW) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return 0, errors.New("closed")
		}
		if next, ok := r.readLocked(); ok {
			r.mu.Unlock()
			n := copy(p, next)
			return n, nil
		}
		ready := r.ready
		r.mu.Unlock()
		<-ready
	}
}

func (r *loopRW) Write(p []byte) (int, error) {
	cp := append([]byte(nil), p...)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, errors.New("closed")
	}
	r.nw += len(p)
	if r.track != nil {
		r.track = append(r.track, cp)
	}
	wasEmpty := len(r.q) == 0
	r.q = append(r.q, cp)
	if wasEmpty {
		close(r.ready)
		r.ready = make(chan struct{})
	}
	r.mu.Unlock()
	return len(p), nil
}

func (r *loopRW) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.ready)
	}
	return nil
}

// captureHandler implements Handler, recording the endpoint IDs of every
// accepted connection so the loopback test can assert the forwarder fired.
type captureHandler struct {
	tcpCh chan TransportEndpointID
}

func (h *captureHandler) HandleTCP(c TCPConn) {
	h.tcpCh <- c.ID()
}

func (h *captureHandler) HandleUDP(UDPConn) {}

// TestNetstackLoopbackForwarder exercises the real TUN-like data path end to
// end: a gonet TCP client's SYN goes out the outbound loop, is read back by the
// dispatch loop (exactly how the kernel's packet enters ntun), and must reach
// the TCP forwarder. This uses no hand-crafted packets.
//
// Two addressing modes:
//   - permanent-dest: the destination fd00:42::42 is a permanent NIC address.
//   - promiscuous-only: the destination is NOT owned by the stack (production
//     case — remote services live on routed networks, not on the tun subnet).
//     gVisor must still deliver the inbound SYN locally via the temp-address
//     mechanism that promiscuous mode enables, so the forwarder sees it.
func TestNetstackLoopbackForwarder(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
	}{
		{"permanent-dest", []string{"fd00:ffff::1", "fd00:42::42"}},
		{"promiscuous-only", []string{"fd00:ffff::1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw := &loopRW{ready: make(chan struct{})}
			h := &captureHandler{tcpCh: make(chan TransportEndpointID, 2)}
			link, err := NewLinkEndpoint(rw, 1500, 0)
			if err != nil {
				t.Fatalf("NewLinkEndpoint: %v", err)
			}
			stk, err := CreateStack(Config{Link: link, Handler: h})
			if err != nil {
				t.Fatalf("CreateStack: %v", err)
			}
			// Teardown contract (same as app.Run): stop the link loops and
			// unblock the device read *before* Destroy, or Destroy hangs.
			defer stk.Destroy() // runs last
			defer rw.Close()
			defer link.Close()

			// Give the stack the client's source address so it can dial. The
			// destination is added only in the permanent-dest case.
			nicID := tcpip.NICID(1)
			for _, addr := range tc.addrs {
				ap := netip.MustParseAddr(addr)
				if err := stk.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
					Protocol: ipv6.ProtocolNumber,
					AddressWithPrefix: tcpip.AddressWithPrefix{
						Address:   tcpip.AddrFrom16(ap.As16()),
						PrefixLen: 64,
					},
				}, stack.AddressProperties{}); err != nil {
					t.Fatalf("AddProtocolAddress(%s): %v", addr, err)
				}
			}

			conn, err := gonet.DialTCP(stk, tcpip.FullAddress{
				NIC:  nicID,
				Addr: tcpip.AddrFrom16(netip.MustParseAddr("fd00:42::42").As16()),
				Port: 80,
			}, ipv6.ProtocolNumber)
			if err != nil {
				t.Fatalf("DialTCP: %v", err)
			}
			defer conn.Close() // runs after Destroy; harmless on a closed stack

			select {
			case id := <-h.tcpCh:
				if got := id.LocalAddress.String(); got != "fd00:42::42" {
					t.Fatalf("forwarder delivered to %s, want fd00:42::42", got)
				}
				if id.LocalPort != 80 {
					t.Fatalf("forwarder delivered to port %d, want 80", id.LocalPort)
				}
			case <-time.After(5 * time.Second):
				ipSt := stk.Stats().IP
				tcpSt := stk.Stats().TCP
				t.Fatalf("SYN never reached forwarder via dispatchLoop: "+
					"IP rx=%d valid=%d delivered=%d malformed=%d invalidDst=%d invalidSrc=%d; "+
					"TCP checksumErrors=%d validSegs=%d invalidSegs=%d; NIC malformedL4=%d; outboundBytes=%d",
					ipSt.PacketsReceived.Value(), ipSt.ValidPacketsReceived.Value(),
					ipSt.PacketsDelivered.Value(), ipSt.MalformedPacketsReceived.Value(),
					ipSt.InvalidDestinationAddressesReceived.Value(), ipSt.InvalidSourceAddressesReceived.Value(),
					tcpSt.ChecksumErrors.Value(), tcpSt.ValidSegmentsReceived.Value(), tcpSt.InvalidSegmentsReceived.Value(),
					stk.Stats().NICs.MalformedL4RcvdPackets.Value(), rw.nw)
			}
		})
	}
}
