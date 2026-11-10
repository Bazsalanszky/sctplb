// SPDX-FileCopyrightText: 2026 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	stdctx "context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/omec-project/sctplb/config"
	"github.com/omec-project/sctplb/context"
)

// Addresses used by the IPv4 and IPv6 discovery and gRPC target tests.
const (
	testIPv4Addr = "10.0.0.1"
	testIPv6Addr = "fd00::1"
)

// fakeDiscovery replaces the resolver and the backend start for one test and
// counts what discovery does.
type fakeDiscovery struct {
	err     error
	ips     []net.IPAddr
	started []string
	names   []string
	lookups int
	// preferIPv6 is what preferIPv6 returns during the test.
	preferIPv6 bool
}

func useFakeDiscovery(t *testing.T, f *fakeDiscovery) {
	t.Helper()
	context.Sctplb_Self().Reset()
	oldLookup, oldStart, oldPrefer := lookupIPAddr, startBackend, preferIPv6
	preferIPv6 = func() bool { return f.preferIPv6 }
	lookupIPAddr = func(_ stdctx.Context, name string) ([]net.IPAddr, error) {
		f.lookups++
		f.names = append(f.names, name)
		return f.ips, f.err
	}
	startBackend = func(_ stdctx.Context, backend context.NF, _ int) {
		f.started = append(f.started, backend.(*GrpcServer).address)
	}
	t.Cleanup(func() {
		lookupIPAddr, startBackend, preferIPv6 = oldLookup, oldStart, oldPrefer
		context.Sctplb_Self().Reset()
	})
}

func grpcService(backendType string, uris ...string) BackendSvc {
	var cfg config.Config
	cfg.Configuration = &config.Configuration{Type: backendType, SctpGrpcPort: 9000}
	for _, uri := range uris {
		cfg.Configuration.Services = append(cfg.Configuration.Services, config.Service{Uri: uri})
	}
	return BackendSvc{Cfg: cfg}
}

// One pass resolves the name once and returns: the loop used to resolve it again
// at once after every successful lookup.
func TestDiscoverService_ResolvesOnceAndAddsNewIPv4Backends(t *testing.T) {
	f := &fakeDiscovery{ips: []net.IPAddr{
		{IP: net.ParseIP("10.0.0.1")},
		{IP: net.ParseIP("10.0.0.2")},
		{IP: net.ParseIP("fd00::1")},
	}}
	useFakeDiscovery(t, f)
	svc := grpcService("grpc")

	svc.discoverService(stdctx.Background(), "amf")
	if f.lookups != 1 {
		t.Fatalf("lookups = %d, want 1", f.lookups)
	}
	if got := context.Sctplb_Self().NFLength(); got != 2 {
		t.Fatalf("backends = %d, want 2 (IPv6 is not used)", got)
	}

	// A second pass finds the same addresses and adds nothing.
	svc.discoverService(stdctx.Background(), "amf")
	if f.lookups != 2 || context.Sctplb_Self().NFLength() != 2 || len(f.started) != 2 {
		t.Fatalf("after a second pass: lookups %d, backends %d, started %v; want 2, 2, 2",
			f.lookups, context.Sctplb_Self().NFLength(), f.started)
	}
}

func TestDiscoverService_FailedLookupAddsNothing(t *testing.T) {
	f := &fakeDiscovery{err: errors.New("no such host")}
	useFakeDiscovery(t, f)

	grpcService("grpc").discoverService(stdctx.Background(), "amf")
	if f.lookups != 1 || context.Sctplb_Self().NFLength() != 0 {
		t.Fatalf("lookups %d, backends %d; want 1, 0", f.lookups, context.Sctplb_Self().NFLength())
	}
}

// An unsupported backend type used to add a nil backend and call it.
func TestDiscoverService_UnsupportedTypeAddsNothing(t *testing.T) {
	f := &fakeDiscovery{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}}
	useFakeDiscovery(t, f)

	grpcService("http").discoverService(stdctx.Background(), "amf")
	if context.Sctplb_Self().NFLength() != 0 || len(f.started) != 0 {
		t.Fatalf("backends %d, started %v; want none", context.Sctplb_Self().NFLength(), f.started)
	}
}

// One pass of the dispatch loop resolves every configured service exactly once,
// in order. The loop used to stay on the first service: after a successful
// lookup it resolved it again at once, and after a failed one it retried it,
// so the services after it were never looked up.
func TestDiscoverAll_ResolvesEachServiceOncePerPass(t *testing.T) {
	for _, tc := range []struct {
		f    *fakeDiscovery
		name string
	}{
		{&fakeDiscovery{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}}, "lookups succeed"},
		{&fakeDiscovery{err: errors.New("no such host")}, "lookups fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeDiscovery(t, tc.f)
			grpcService("grpc", "amf", "amf-2").discoverAll(stdctx.Background())
			if len(tc.f.names) != 2 || tc.f.names[0] != "amf" || tc.f.names[1] != "amf-2" {
				t.Fatalf("looked up %v, want [amf amf-2]", tc.f.names)
			}
		})
	}
}

// On an IPv6-only cluster the service resolves to AAAA records only. Discovery
// used to drop every address that was not IPv4, so no backend was ever added.
func TestDiscoverService_AddsIPv6BackendsWhenThereIsNoIPv4(t *testing.T) {
	f := &fakeDiscovery{ips: []net.IPAddr{
		{IP: net.ParseIP(testIPv6Addr)},
		{IP: net.ParseIP("fd00::2")},
	}}
	useFakeDiscovery(t, f)
	svc := grpcService("grpc")

	svc.discoverService(stdctx.Background(), "amf")
	if got, want := f.started, []string{testIPv6Addr, "fd00::2"}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}

	// A second pass finds the same addresses and adds nothing.
	svc.discoverService(stdctx.Background(), "amf")
	if context.Sctplb_Self().NFLength() != 2 || len(f.started) != 2 {
		t.Fatalf("after a second pass: backends %d, started %v; want 2, 2",
			context.Sctplb_Self().NFLength(), f.started)
	}
}

func TestBackendAddresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []net.IPAddr
		want []string
	}{
		{
			name: "IPv4 only",
			ips:  []net.IPAddr{{IP: net.ParseIP(testIPv4Addr)}, {IP: net.ParseIP("10.0.0.2")}},
			want: []string{testIPv4Addr, "10.0.0.2"},
		},
		{
			name: "IPv6 only",
			ips:  []net.IPAddr{{IP: net.ParseIP(testIPv6Addr)}, {IP: net.ParseIP("fd00::2")}},
			want: []string{testIPv6Addr, "fd00::2"},
		},
		{
			name: "dual-stack prefers IPv4",
			ips: []net.IPAddr{
				{IP: net.ParseIP(testIPv6Addr)},
				{IP: net.ParseIP(testIPv4Addr)},
				{IP: net.ParseIP("fd00::2")},
				{IP: net.ParseIP("10.0.0.2")},
			},
			want: []string{testIPv4Addr, "10.0.0.2"},
		},
		{
			name: "4-byte IPv4",
			ips:  []net.IPAddr{{IP: net.IPv4(10, 0, 0, 1).To4()}},
			want: []string{testIPv4Addr},
		},
		{
			name: "IPv4-mapped IPv6 counts as IPv4",
			ips:  []net.IPAddr{{IP: net.ParseIP("::ffff:10.0.0.1")}, {IP: net.ParseIP(testIPv6Addr)}},
			want: []string{testIPv4Addr},
		},
		{
			name: "IPv6 in canonical form",
			ips:  []net.IPAddr{{IP: net.ParseIP("FD00:0000:0000::0001")}},
			want: []string{testIPv6Addr},
		},
		{
			name: "invalid addresses are skipped",
			ips:  []net.IPAddr{{}, {IP: net.IP{10, 0, 0}}, {IP: net.ParseIP(testIPv6Addr)}},
			want: []string{testIPv6Addr},
		},
		{name: "nothing resolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendAddresses(tc.ips, false); !slices.Equal(got, tc.want) {
				t.Errorf("backendAddresses(%v) = %v, want %v", tc.ips, got, tc.want)
			}
		})
	}
}
