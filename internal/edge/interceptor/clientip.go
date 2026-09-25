package interceptor

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// TrustedProxies decides whose X-Forwarded-For may be believed.
//
// The REST gateway used gin's ClientIP without SetTrustedProxies, and gin's
// default trusts EVERY address (gin.go: defaultTrustedCIDRs is 0.0.0.0/0 and
// ::/0). Any client could therefore send a new X-Forwarded-For on every
// request and get a fresh rate-limit counter each time, which made the
// five-per-minute login limit decorative. Proven red before this was written:
// six attempts from one client with six invented headers were all accepted.
//
// Here the header is believed ONLY when the direct peer is a configured
// proxy. The empty list - the default - believes nobody and uses the peer
// address, which is right when the gateway is reached directly.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies reads a comma-separated list of CIDRs.
//
// A malformed entry is an error, not a skipped value: a typo that silently
// drops the load balancer's range would put every user behind one address
// and one shared counter.
func ParseTrustedProxies(raw string) (TrustedProxies, error) {
	var out TrustedProxies
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			return TrustedProxies{}, fmt.Errorf("trusted proxy %q is not a CIDR: %w", part, err)
		}
		out.nets = append(out.nets, cidr)
	}
	return out, nil
}

func (t TrustedProxies) trusts(ip net.IP) bool {
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to attribute a request to.
//
// When the peer is trusted, X-Forwarded-For is read from the RIGHT, skipping
// trusted hops: the rightmost entries were appended by our own proxies, and
// anything to the left of the first untrusted entry was written by the
// client and proves nothing. An unreadable address becomes "unknown" and
// shares one counter - too strict for those callers, and right: a limit that
// leaks because one address failed to parse protects nothing.
func (t TrustedProxies) ClientIP(peerAddr string, header http.Header) string {
	peer := parseHost(peerAddr)
	if peer == nil {
		return "unknown"
	}
	if !t.trusts(peer) {
		return peer.String()
	}

	hops := header.Values("X-Forwarded-For")
	var entries []string
	for _, h := range hops {
		entries = append(entries, strings.Split(h, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(entries[i]))
		if ip == nil {
			return "unknown"
		}
		if !t.trusts(ip) {
			return ip.String()
		}
	}
	return peer.String()
}

func parseHost(addr string) net.IP {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	return net.ParseIP(host)
}
