// SPDX-FileCopyrightText: 2026 Balázs Toldi
//
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	stdctx "context"
	"io"
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/omec-project/sctplb/context"
	gClient "github.com/omec-project/sctplb/sdcoreAmfServer"
	"google.golang.org/grpc"
)

// Other spellings of testIPv4Addr and testIPv6Addr, a pod IP list, and other
// addresses and names used by the address and redirect tests.
const (
	testMappedIPv4 = "::ffff:" + testIPv4Addr
	testLongIPv6   = "fd00:0000:0:0::0001"
	testZonedIPv6  = testIPv6Addr + "%eth0"
	testPodIPs     = testIPv4Addr + "," + testIPv6Addr
	testOtherIPv4  = "10.0.0.9"
	testIPv4Addr2  = "10.0.0.2"
	testIPv6Addr2  = "fd00::2"
	testAmfName    = "amf-0"
)

func TestParseHostAddr(t *testing.T) {
	v4, v6 := netip.MustParseAddr(testIPv4Addr), netip.MustParseAddr(testIPv6Addr)
	for _, tc := range []struct {
		want netip.Addr
		in   string
		ok   bool
	}{
		{in: testIPv4Addr, want: v4, ok: true},
		{in: testIPv4Addr + ":5000", want: v4, ok: true},
		{in: testMappedIPv4, want: v4, ok: true},
		{in: "[" + testMappedIPv4 + "]:5000", want: v4, ok: true},
		{in: testIPv6Addr, want: v6, ok: true},
		{in: testLongIPv6, want: v6, ok: true},
		{in: "FD00::1", want: v6, ok: true},
		{in: "[" + testIPv6Addr + "]", want: v6, ok: true},
		{in: "[" + testIPv6Addr + "]:5000", want: v6, ok: true},
		{in: testZonedIPv6, want: v6, ok: true},
		{in: "[" + testZonedIPv6 + "]:5000", want: v6, ok: true},
		{in: " " + testIPv6Addr + "\n", want: v6, ok: true},
		{in: ""},
		{in: testAmfName},
		{in: testAmfName + ":5000"},
		{in: "10.0.0.256"},
		{in: "[" + testIPv4Addr},
		{in: testIPv6Addr + "]"},
		// Without brackets, a port cannot be told apart from the last group.
		{in: testIPv6Addr + ":5000", want: netip.MustParseAddr("fd00::1:5000"), ok: true},
	} {
		got, ok := parseHostAddr(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseHostAddr(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseAddrList(t *testing.T) {
	v4, v6 := netip.MustParseAddr(testIPv4Addr), netip.MustParseAddr(testIPv6Addr)
	for _, tc := range []struct {
		in   string
		want []netip.Addr
	}{
		{in: testIPv4Addr, want: []netip.Addr{v4}},
		{in: testPodIPs, want: []netip.Addr{v4, v6}},
		{in: " " + testLongIPv6 + " , " + testMappedIPv4 + " ", want: []netip.Addr{v6, v4}},
		{in: testIPv4Addr + ",," + testAmfName + "," + testIPv6Addr, want: []netip.Addr{v4, v6}},
		{in: ""},
		{in: testAmfName},
	} {
		if got := parseAddrList(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("parseAddrList(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPrimaryIsIPv6(t *testing.T) {
	for _, tc := range []struct {
		podIP, podIPs string
		want          bool
	}{
		{podIP: testIPv4Addr, want: false},
		{podIP: testIPv6Addr, want: true},
		{podIP: testMappedIPv4, want: false},
		{podIP: testIPv4Addr, podIPs: testIPv6Addr + "," + testIPv4Addr, want: false},
		{podIP: testIPv6Addr, podIPs: testPodIPs, want: true},
		{podIPs: testIPv6Addr + "," + testIPv4Addr, want: true},
		{podIPs: testPodIPs, want: false},
		{podIP: "bogus", podIPs: testIPv6Addr, want: true},
		{want: false},
	} {
		if got := primaryIsIPv6(tc.podIP, tc.podIPs); got != tc.want {
			t.Errorf("primaryIsIPv6(%q, %q) = %v, want %v", tc.podIP, tc.podIPs, got, tc.want)
		}
	}
}

func TestBackendAddresses_PreferIPv6(t *testing.T) {
	dual := []net.IPAddr{
		{IP: net.ParseIP(testIPv4Addr)},
		{IP: net.ParseIP(testIPv6Addr)},
		{IP: net.ParseIP(testIPv4Addr2)},
		{IP: net.ParseIP(testIPv6Addr2)},
	}
	for _, tc := range []struct {
		name     string
		ips      []net.IPAddr
		want     []string
		preferV6 bool
	}{
		{name: "dual-stack, IPv4 primary", ips: dual, want: []string{testIPv4Addr, testIPv4Addr2}},
		{name: "dual-stack, IPv6 primary", ips: dual, preferV6: true, want: []string{testIPv6Addr, testIPv6Addr2}},
		{
			name: "IPv6 primary, IPv4-only answer", preferV6: true,
			ips:  []net.IPAddr{{IP: net.ParseIP(testIPv4Addr)}},
			want: []string{testIPv4Addr},
		},
		{
			name: "IPv6 primary, IPv4-mapped is not IPv6", preferV6: true,
			ips:  []net.IPAddr{{IP: net.ParseIP(testMappedIPv4)}},
			want: []string{testIPv4Addr},
		},
		{
			name: "IPv4 primary, IPv6-only answer",
			ips:  []net.IPAddr{{IP: net.ParseIP(testIPv6Addr)}},
			want: []string{testIPv6Addr},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendAddresses(tc.ips, tc.preferV6); !slices.Equal(got, tc.want) {
				t.Errorf("backendAddresses(%v, %v) = %v, want %v", tc.ips, tc.preferV6, got, tc.want)
			}
		})
	}
}

func TestFindRedirectBackend(t *testing.T) {
	backends := []*GrpcServer{
		{address: testOtherIPv4},
		{address: testIPv4Addr},
		{address: testIPv6Addr},
		{address: testAmfName},
	}
	for _, tc := range []struct {
		redirectID string
		want       string // address of the backend found, "" for none
	}{
		{redirectID: testIPv4Addr, want: testIPv4Addr},
		{redirectID: testMappedIPv4, want: testIPv4Addr},
		{redirectID: testIPv4Addr + ":9000", want: testIPv4Addr},
		{redirectID: testIPv6Addr, want: testIPv6Addr},
		{redirectID: testLongIPv6, want: testIPv6Addr},
		{redirectID: "FD00::1", want: testIPv6Addr},
		{redirectID: "[" + testIPv6Addr + "]:9000", want: testIPv6Addr},
		{redirectID: testZonedIPv6, want: testIPv6Addr},
		// A list of the target's pod IPs matches on any of them.
		{redirectID: "fd00::9," + testIPv4Addr, want: testIPv4Addr},
		{redirectID: "10.0.0.8," + testLongIPv6, want: testIPv6Addr},
		// A value that is not an IP address still matches as a string.
		{redirectID: testAmfName, want: testAmfName},
		{redirectID: "10.0.0.8"},
		{redirectID: "fd00::9"},
		{redirectID: "amf-1"},
		{redirectID: ""},
	} {
		got := findRedirectBackend(backends, tc.redirectID)
		var gotAddr string
		if got != nil {
			gotAddr = got.address
		}
		if gotAddr != tc.want {
			t.Errorf("findRedirectBackend(%q) found %q, want %q", tc.redirectID, gotAddr, tc.want)
		}
	}
}

// fakeStream is a backend's gRPC stream: Recv returns recv in order and then
// io.EOF, and Send records what was sent.
type fakeStream struct {
	grpc.ClientStream
	recv []*gClient.AmfMessage
	sent []*gClient.SctplbMessage
}

func (s *fakeStream) Send(m *gClient.SctplbMessage) error {
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeStream) Recv() (*gClient.AmfMessage, error) {
	if len(s.recv) == 0 {
		return nil, io.EOF
	}
	m := s.recv[0]
	s.recv = s.recv[1:]
	return m, nil
}

// An AMF redirects a message by naming the owner AMF's POD_IP. readFromServer
// forwards it to the backend with that address. It used to compare the two as
// strings, so any other spelling of the address, or the owner's list of pod
// IPs, dropped the message.
func TestReadFromServer_ForwardsRedirectToMatchingBackend(t *testing.T) {
	for _, tc := range []struct {
		target, redirectID string
		forwarded          bool
	}{
		{target: testIPv4Addr, redirectID: testIPv4Addr, forwarded: true},
		{target: testIPv4Addr, redirectID: testMappedIPv4, forwarded: true},
		{target: testIPv6Addr, redirectID: testIPv6Addr, forwarded: true},
		{target: testIPv6Addr, redirectID: testLongIPv6, forwarded: true},
		{target: testIPv6Addr, redirectID: testPodIPs, forwarded: true},
		{target: testIPv4Addr, redirectID: testOtherIPv4},
		// A dual-stack AMF with an IPv6 POD_IP behind an IPv4 backend address
		// does not match: discovery avoids this with preferIPv6.
		{target: testIPv4Addr, redirectID: testIPv6Addr},
	} {
		t.Run(tc.target+" "+tc.redirectID, func(t *testing.T) {
			context.Sctplb_Self().Reset()
			t.Cleanup(context.Sctplb_Self().Reset)

			msg := []byte{0x00, 0x0e}
			source := &GrpcServer{address: "10.0.0.7", readDone: make(chan struct{}), stream: &fakeStream{
				recv: []*gClient.AmfMessage{{
					Msgtype:    gClient.MsgType_REDIRECT_MSG,
					RedirectId: tc.redirectID,
					GnbId:      "gnb-1",
					Msg:        msg,
				}},
			}}
			targetStream := &fakeStream{}
			target := &GrpcServer{address: tc.target, stream: targetStream}
			target.state.Store(true)
			context.Sctplb_Self().AddNF(source)
			context.Sctplb_Self().AddNF(target)

			source.readFromServer()

			if !tc.forwarded {
				if len(targetStream.sent) != 0 {
					t.Fatalf("forwarded %v, want nothing", targetStream.sent)
				}
				return
			}
			if len(targetStream.sent) != 1 {
				t.Fatalf("forwarded %d messages, want 1", len(targetStream.sent))
			}
			got := targetStream.sent[0]
			if got.Msgtype != gClient.MsgType_GNB_MSG || got.GnbId != "gnb-1" || !slices.Equal(got.Msg, msg) {
				t.Errorf("forwarded %v, want a GNB_MSG for gnb-1 with %x", got, msg)
			}
		})
	}
}

// On a dual-stack cluster whose primary family is IPv6, every pod's POD_IP is
// IPv6, and so is the redirect target an AMF sends. Discovery then takes the
// IPv6 backend addresses, so the redirect target is found.
func TestDiscoverService_IPv6PrimaryRedirectFindsBackend(t *testing.T) {
	f := &fakeDiscovery{preferIPv6: true, ips: []net.IPAddr{
		{IP: net.ParseIP(testIPv4Addr)},
		{IP: net.ParseIP(testIPv6Addr)},
		{IP: net.ParseIP(testIPv4Addr2)},
		{IP: net.ParseIP(testIPv6Addr2)},
	}}
	useFakeDiscovery(t, f)

	grpcService("grpc").discoverService(stdctx.Background(), "amf-headless")
	if want := []string{testIPv6Addr, testIPv6Addr2}; !slices.Equal(f.started, want) {
		t.Fatalf("started %v, want %v", f.started, want)
	}
	got := findRedirectBackend(grpcBackends(), "fd00:0:0::2")
	if got == nil || got.address != testIPv6Addr2 {
		t.Fatalf("findRedirectBackend(fd00:0:0::2) = %v, want the %s backend", got, testIPv6Addr2)
	}
}
