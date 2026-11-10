// SPDX-FileCopyrightText: 2026 Balázs Toldi
//
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	stdctx "context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGrpcTarget(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    string
	}{
		{address: testIPv4Addr, want: testIPv4Addr + ":9000"},
		{address: testIPv6Addr, want: "[" + testIPv6Addr + "]:9000"},
		{address: "amf", want: "amf:9000"},
	} {
		if got := grpcTarget(tc.address, 9000); got != tc.want {
			t.Errorf("grpcTarget(%q, 9000) = %q, want %q", tc.address, got, tc.want)
		}
	}
}

// The target that ConnectToServer builds must reach a gRPC server listening on
// the backend address, for both address families. With "%s:%d", "::1:<port>"
// is itself an IPv6 address, which the gRPC DNS resolver dials on port 443.
func TestGrpcTarget_ConnectsToServer(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 5*time.Second)
			defer cancel()

			var lc net.ListenConfig
			lis, err := lc.Listen(ctx, "tcp", net.JoinHostPort(address, "0"))
			if err != nil {
				t.Skipf("no loopback for %s: %v", address, err)
			}
			srv := grpc.NewServer()
			serveErr := make(chan error, 1)
			go func() { serveErr <- srv.Serve(lis) }()
			t.Cleanup(func() {
				srv.Stop()
				if serr := <-serveErr; serr != nil {
					t.Errorf("grpc server: %v", serr)
				}
			})

			port := lis.Addr().(*net.TCPAddr).Port
			conn, err := grpc.NewClient(grpcTarget(address, port),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("grpc.NewClient: %v", err)
			}
			t.Cleanup(func() {
				if cerr := conn.Close(); cerr != nil {
					t.Errorf("close client: %v", cerr)
				}
			})

			conn.Connect()
			for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
				if !conn.WaitForStateChange(ctx, state) {
					t.Fatalf("connection to %s did not become ready, last state %v", grpcTarget(address, port), state)
				}
			}
		})
	}
}
