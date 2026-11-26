// Package checks holds the wizard's live checks: DNS, ports, SMTP.
package checks

import (
	"net"
	"sort"
)

// LocalIPs: this host's global unicast addresses (the setup container runs
// on the host network, so these are the host's).
func LocalIPs() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		out = append(out, ip)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Public before private, IPv4 before IPv6.
		pi, pj := !out[i].IsPrivate(), !out[j].IsPrivate()
		if pi != pj {
			return pi
		}
		return out[i].To4() != nil && out[j].To4() == nil
	})
	return out
}

// PrimaryIP: the source address of the default route (no packet is sent),
// else the first local address.
func PrimaryIP() net.IP {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if ua, ok := c.LocalAddr().(*net.UDPAddr); ok && !ua.IP.IsLoopback() {
			return ua.IP
		}
	}
	if ips := LocalIPs(); len(ips) > 0 {
		return ips[0]
	}
	return nil
}
