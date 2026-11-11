package agentrun

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// listeners returns the addresses listening (state 0A) on port in the
// content of /proc/net/tcp or tcp6, in file order.
func listeners(data []byte, port int) []net.IP {
	var out []net.IP
	sc := bufio.NewScanner(bytes.NewReader(data))
	want := fmt.Sprintf("%04X", port)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		addr, p, ok := strings.Cut(f[1], ":")
		if !ok || !strings.EqualFold(p, want) {
			continue
		}
		raw, err := hex.DecodeString(addr)
		if err != nil || (len(raw) != 4 && len(raw) != 16) {
			continue
		}
		// each 32-bit word is written in host (little-endian) order
		ip := make(net.IP, len(raw))
		for i := 0; i < len(raw); i += 4 {
			for j := 0; j < 4; j++ {
				ip[i+j] = raw[i+3-j]
			}
		}
		out = append(out, ip)
	}
	return out
}

// ListenWarnings checks, from the content of /proc/net/tcp and tcp6, that
// something listens on each port where an agent's connection can arrive.
// It returns one warning per port that nothing serves, or only on addresses
// agents cannot reach, in port order.
func ListenWarnings(tcp, tcp6 []byte, ports []int) []string {
	var out []string
	for _, port := range sortedPorts(ports) {
		ips := append(listeners(tcp, port), listeners(tcp6, port)...)
		if len(ips) == 0 {
			out = append(out, fmt.Sprintf("port %d: nothing listens on it yet; agents are refused until a service does", port))
			continue
		}
		var addrs []string
		seen := map[string]bool{}
		wildcard, allLoopback := false, true
		for _, ip := range ips {
			s := ip.String()
			if !seen[s] {
				seen[s] = true
				addrs = append(addrs, s)
			}
			if ip.IsUnspecified() {
				wildcard = true
			}
			if !ip.IsLoopback() {
				allLoopback = false
			}
		}
		switch {
		case wildcard:
		case allLoopback:
			out = append(out, fmt.Sprintf(`port %d: listens only on %s, which agents can't reach; publish it on all interfaces (in compose: "%d:%d", not "127.0.0.1:%d:%d")`,
				port, strings.Join(addrs, ","), port, port, port, port))
		default:
			out = append(out, fmt.Sprintf("port %d: listens only on %s; agents connect to host.jauntbox (the Docker bridge gateway), so bind it to all interfaces",
				port, strings.Join(addrs, ",")))
		}
	}
	return out
}
