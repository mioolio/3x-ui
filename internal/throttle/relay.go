package throttle

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// RelayTCPPort is the fixed loopback port of the rate-limiting SOCKS5 relay.
// Every per-user throttle outbound in the generated Xray config points here.
const RelayTCPPort = 64210

// EgressInboundPort is the loopback port of the Xray "throttle-egress" SOCKS
// inbound the relay dials through so limited traffic egresses exactly like
// everyone else's (default outbound, admin routing, DNS handled by the core).
const EgressInboundPort = 64220

// EgressAddr is the loopback address of the Xray throttle-egress inbound.
var EgressAddr = fmt.Sprintf("127.0.0.1:%d", EgressInboundPort)

const udpIdleTimeout = 90 * time.Second

// relay is one listening SOCKS5 server on loopback. Username/password auth
// carries the client email, which selects the shared per-client buckets.
type relay struct {
	mu      sync.Mutex
	ln      net.Listener
	egress  string
	mgr     *Manager
	closing chan struct{}
	wg      sync.WaitGroup
}

func newRelay(egress string, mgr *Manager) *relay {
	return &relay{egress: egress, mgr: mgr, closing: make(chan struct{})}
}

// listening reports whether the accept loop is live.
func (r *relay) listening() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ln != nil
}

// start binds the loopback listener; a bind failure (usually another process
// squatting on the port) is reported so the caller can drop the Xray-side
// injection instead of silently mis-routing user traffic.
func (r *relay) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln != nil {
		return nil
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", RelayTCPPort))
	if err != nil {
		return fmt.Errorf("throttle: relay listen: %w", err)
	}
	r.ln = ln
	r.closing = make(chan struct{})
	logger.Infof("throttle: relay listening on %s", ln.Addr())
	r.wg.Add(1)
	go r.acceptLoop(ln)
	return nil
}

func (r *relay) stop() {
	r.mu.Lock()
	ln := r.ln
	r.ln = nil
	if ln == nil {
		r.mu.Unlock()
		return
	}
	close(r.closing)
	r.mu.Unlock()
	ln.Close()
	r.wg.Wait()
}

func (r *relay) acceptLoop(ln net.Listener) {
	defer r.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-r.closing:
				return
			default:
			}
			logger.Warningf("throttle: relay accept: %v", err)
			continue
		}
		select {
		case <-r.closing:
			conn.Close()
			return
		default:
		}
		r.wg.Add(1)
		go func(c net.Conn) {
			defer r.wg.Done()
			defer c.Close()
			r.handleConn(c)
		}(conn)
	}
}

func (r *relay) handleConn(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(socksHandshakeTimeout))
	email, ok := socksGreeting(conn)
	if !ok || email == "" {
		return
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return
	}
	target, err := readSocksTarget(conn, head[3])
	if err != nil {
		writeSocksReply(conn, 0x01)
		return
	}
	down, up := r.mgr.bucketsFor(email)
	if down == nil && up == nil {
		// Unknown or just-unthrottled user: nothing should be routed here.
		writeSocksReply(conn, 0x05)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	switch head[1] {
	case 0x01:
		r.relayTCP(conn, email, target, down, up)
	case 0x03:
		r.relayUDP(conn, email, down, up)
	default:
		writeSocksReply(conn, 0x07)
	}
}

// relayTCP dials the target through the egress hop and pumps both directions
// through the per-client buckets: upload is client->egress, download the way
// back.
func (r *relay) relayTCP(client net.Conn, email string, target socksTarget, down, up *bucket) {
	egress, err := socksDialTCP(r.egress, target, net.Dial)
	if err != nil {
		logger.Debugf("throttle: %s: egress dial %s: %v", email, target, err)
		writeSocksReply(client, 0x04)
		return
	}
	defer egress.Close()
	writeSocksReply(client, 0x00)
	done := make(chan struct{}, 2)
	go func() {
		limitedCopy(egress, client, up)
		done <- struct{}{}
	}()
	go func() {
		limitedCopy(client, egress, down)
		done <- struct{}{}
	}()
	<-done
}

// limitedCopy copies src into dst, charging every read against the bucket.
func limitedCopy(dst, src net.Conn, b *bucket) {
	buf := make([]byte, 16*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			b.wait(int64(n))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

// relayUDP answers a UDP ASSOCIATE with a per-association host-facing socket
// and relays datagrams through a client-side associate to the egress hop,
// charging payload bytes against the same per-client buckets.
func (r *relay) relayUDP(control net.Conn, email string, down, up *bucket) {
	hostSock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		logger.Debugf("throttle: %s: udp bind: %v", email, err)
		writeSocksReply(control, 0x01)
		return
	}
	defer hostSock.Close()
	assoc, err := socksUDPAssociate(r.egress, net.Dial)
	if err != nil {
		logger.Debugf("throttle: %s: egress associate: %v", email, err)
		writeSocksReply(control, 0x01)
		return
	}
	defer assoc.close()

	local := hostSock.LocalAddr().(*net.UDPAddr)
	ip4 := local.IP.To4()
	reply := []byte{0x05, 0x00, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], byte(local.Port >> 8), byte(local.Port)}
	if _, err := control.Write(reply); err != nil {
		return
	}

	var clientAddr *net.UDPAddr
	var clientMu sync.Mutex

	go func() {
		buf := make([]byte, 65536)
		for {
			_ = assoc.sock.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			target, payload, rerr := assoc.receive(buf)
			if rerr != nil {
				return
			}
			clientMu.Lock()
			dst := clientAddr
			clientMu.Unlock()
			if dst == nil {
				continue
			}
			down.wait(int64(len(payload)))
			dg := buildDatagramHeader(target)
			dg = append(dg, payload...)
			if _, werr := hostSock.WriteToUDP(dg, dst); werr != nil {
				return
			}
		}
	}()

	buf := make([]byte, 65536)
	for {
		n, from, rerr := hostSock.ReadFromUDP(buf)
		if rerr != nil {
			return
		}
		clientMu.Lock()
		if clientAddr == nil {
			clientAddr = from
		} else if clientAddr.String() != from.String() {
			// RFC 1928: datagrams must come from the associated client only.
			clientMu.Unlock()
			continue
		}
		clientMu.Unlock()
		target, off, ok := parseDatagramHeader(buf[:n])
		if !ok {
			continue
		}
		payload := buf[off:n]
		up.wait(int64(len(payload)))
		if err := assoc.send(target, payload); err != nil {
			return
		}
	}
}
