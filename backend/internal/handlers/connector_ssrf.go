package handlers

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// SSRF guard for connector outbound requests. Data connectors legitimately point
// at user-supplied hosts (reaching an internal Trino/Postgres/MySQL is the whole
// point), so we deliberately do NOT block private/RFC1918 ranges. We DO block
// link-local addresses — crucially the cloud instance-metadata endpoints
// (169.254.169.254, fd00:ec2::254) — which are never a valid database and are the
// classic SSRF exfiltration target.

// ssrfBlocked reports whether an IP must never be dialed for a connector request.
func ssrfBlocked(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// ssrfDialControl rejects a connection at dial time when the resolved peer IP is
// blocked — applied AFTER DNS resolution, so a hostname can't smuggle past the
// check (the net.Dialer hands Control the concrete resolved IP:port).
func ssrfDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if ip := net.ParseIP(host); ip != nil && ssrfBlocked(ip) {
		return fmt.Errorf("connector: refusing to connect to blocked address %s", host)
	}
	return nil
}

// guardedConnectorHTTPClient builds an http.Client whose dialer rejects
// link-local/metadata peers. insecure skips TLS verification (self-signed dev
// certs), matching the previous behaviour.
func guardedConnectorHTTPClient(timeout time.Duration, insecure bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: ssrfDialControl}
	tr := &http.Transport{DialContext: d.DialContext}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// ssrfCheckConnectorURL resolves the host of a (jdbc) connector URL and errors if
// any resolved IP is blocked. Best-effort pre-check for the JDBC drivers, whose
// dialer we can't hook the way we do for HTTP.
func ssrfCheckConnectorURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimPrefix(raw, "jdbc:"))
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ssrfBlocked(ip) {
			return fmt.Errorf("connector: refusing to connect to blocked address %s", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil // let the real connection surface the resolution error
	}
	for _, ip := range ips {
		if ssrfBlocked(ip) {
			return fmt.Errorf("connector: refusing to connect to blocked address %s (%s)", host, ip)
		}
	}
	return nil
}
