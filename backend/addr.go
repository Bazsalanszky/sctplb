// SPDX-FileCopyrightText: 2026 Balázs Toldi
//
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/omec-project/sctplb/logger"
)

// parseHostAddr parses one IP address in any of the forms an AMF or the
// Kubernetes downward API may report it: a bare IPv4 or IPv6 literal in any
// spelling, with or without a zone, in brackets, or with a port. The result is
// unmapped (::ffff:a.b.c.d becomes a.b.c.d) and has no zone, so two spellings
// of the same address compare equal with ==. A zone only names an interface
// on the host that reported the address, so it is not part of the identity of
// a pod address seen from here.
func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	addr, err := netip.ParseAddr(s)
	if err != nil {
		if addrPort, perr := netip.ParseAddrPort(s); perr == nil {
			addr, err = addrPort.Addr(), nil
		} else if inner, ok := strings.CutPrefix(s, "["); ok && strings.HasSuffix(inner, "]") {
			addr, err = netip.ParseAddr(strings.TrimSuffix(inner, "]"))
		}
	}
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// parseAddrList parses a comma-separated list of addresses, the form of
// status.podIPs, with parseHostAddr. A single address is a list of one.
// Entries that are not IP addresses are skipped.
func parseAddrList(s string) []netip.Addr {
	var addrs []netip.Addr
	for entry := range strings.SplitSeq(s, ",") {
		if addr, ok := parseHostAddr(entry); ok {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// isAddress reports whether the backend is the AMF instance that target names.
// target is an address list as parseAddrList reads it: today the target AMF's
// POD_IP, possibly every one of its pod IPs. The backend matches if its address
// equals any of them, whatever their spelling or family mapping. A target that
// is equal as a string still matches, so a value that is not an IP address
// behaves as before.
func (b *GrpcServer) isAddress(target string) bool {
	if b.address == target {
		return true
	}
	own, ok := parseHostAddr(b.address)
	return ok && slices.Contains(parseAddrList(target), own)
}

// findRedirectBackend returns the first backend whose address is the redirect
// target, or nil if there is none.
func findRedirectBackend(backends []*GrpcServer, redirectID string) *GrpcServer {
	for _, b := range backends {
		if b.isAddress(redirectID) {
			return b
		}
	}
	return nil
}

// primaryIsIPv6 reports whether this pod's primary address is IPv6: POD_IP
// (status.podIP) if it holds an address, else the first address in POD_IPS
// (status.podIPs). Every pod of a cluster has the same primary family, so this
// is also the family of the POD_IP an AMF sends as the redirect target.
func primaryIsIPv6(podIP, podIPs string) bool {
	addrs := parseAddrList(podIP)
	if len(addrs) == 0 {
		addrs = parseAddrList(podIPs)
	}
	return len(addrs) > 0 && addrs[0].Is6()
}

// preferIPv6 reports whether discovery prefers IPv6 backend addresses, which is
// when this pod's own primary address is IPv6. It reads the environment once.
// It is a variable so that tests can replace it.
var preferIPv6 = sync.OnceValue(func() bool {
	podIP, podIPs := os.Getenv("POD_IP"), os.Getenv("POD_IPS")
	v6 := primaryIsIPv6(podIP, podIPs)
	family := "IPv4"
	if v6 {
		family = "IPv6"
	}
	logger.DiscoveryLog.Infof("POD_IP %q, POD_IPS %q: preferring %s backend addresses", podIP, podIPs, family)
	return v6
})
