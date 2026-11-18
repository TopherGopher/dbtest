package hostaddr

import "net"

// BindIP returns the interface to publish on for the address the caller will
// dial. Docker publishes on exactly the address it is given, so a port bound
// to loopback is refused from anywhere else.
//
// Loopback stays loopback, which keeps a laptop's containers off the network. Any
// other address gets every interface, because the address a caller dials,
// such as a remote daemon's hostname, need not be one that host can bind.
func BindIP(host string) string {
	if IsLoopback(host) {
		return Loopback
	}
	return AllInterfaces
}

// DialHost returns the address to dial for a resolved one. Every loopback IP
// becomes 127.0.0.1, the one address a loopback publish listens on; ::1 and
// 127.0.1.1 would be refused. A name such as localhost is left alone: a
// dialler tries each address it resolves to, so it finds 127.0.0.1 itself.
func DialHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return Loopback
	}
	return host
}

// IsLoopback reports whether a host is this machine: localhost, or any
// loopback IP.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
