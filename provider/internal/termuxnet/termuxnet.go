// Package termuxnet builds the HTTP client the proxy makes upstream calls with.
//
// It exists for one reason: Android has no /etc/resolv.conf, so Go's pure
// resolver falls back to asking localhost and every lookup fails with "server
// misbehaving". Termux keeps its own copy under $PREFIX/etc, and the engine's
// own bootstrapper works around the same problem by forcing GODEBUG=netdns=cgo.
// A cgo-free binary has to do it in code instead.
package termuxnet

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FallbackServers are used when no resolv.conf can be read at all.
var FallbackServers = []string{"1.1.1.1:53", "8.8.8.8:53"}

// resolvPaths are searched in order.
func resolvPaths() []string {
	var out []string
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		out = append(out, filepath.Join(prefix, "etc", "resolv.conf"))
	}
	return append(out, "/etc/resolv.conf")
}

// Nameservers reads the nameserver lines of the first resolv.conf that exists.
func Nameservers() []string {
	for _, path := range resolvPaths() {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var servers []string
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 && fields[0] == "nameserver" {
				servers = append(servers, net.JoinHostPort(fields[1], "53"))
			}
		}
		_ = f.Close()
		if len(servers) > 0 {
			return servers
		}
	}
	return FallbackServers
}

// needsOwnResolver reports whether the platform's own resolver configuration is
// missing, which is the case on Android and not on a normal Linux host.
func needsOwnResolver() bool {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return false
	}
	return true
}

// Resolver returns a resolver that asks the servers Termux knows about, or nil
// when the host's own configuration is fine.
func Resolver() *net.Resolver {
	if !needsOwnResolver() {
		return nil
	}
	servers := Nameservers()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			d := net.Dialer{Timeout: 5 * time.Second}
			for _, s := range servers {
				conn, err := d.DialContext(ctx, network, s)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

// Client is an HTTP client suitable for long streaming replies: no overall
// timeout, because a model can think for minutes, but real timeouts on every
// phase that should not take minutes.
func Client() *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  Resolver(),
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   4,
			ForceAttemptHTTP2:     true,
		},
	}
}
