package grpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/hashicorp/consul/agent/grpc/internal/testservice"
	"github.com/hashicorp/consul/agent/metadata"
	"github.com/hashicorp/consul/agent/pool"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type testServer struct {
	addr     net.Addr
	name     string
	dc       string
	shutdown func()
}

func (s testServer) Metadata() *metadata.Server {
	return &metadata.Server{ID: s.name, Datacenter: s.dc, Addr: s.addr}
}

func newTestServer(t *testing.T, name string, dc string) testServer {
	addr := &net.IPAddr{IP: net.ParseIP("127.0.0.1")}
	handler := NewHandler(addr)

	testservice.RegisterSimpleServer(handler.srv, &simple{name: name, dc: dc})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	rpc := &fakeRPCListener{t: t, handler: handler}

	g := errgroup.Group{}
	g.Go(func() error {
		return rpc.listen(lis)
	})
	g.Go(func() error {
		return handler.Run()
	})
	return testServer{
		addr: lis.Addr(),
		name: name,
		dc:   dc,
		shutdown: func() {
			if err := lis.Close(); err != nil {
				t.Logf("listener closed with error: %v", err)
			}
			if err := handler.Shutdown(); err != nil {
				t.Logf("grpc server shutdown: %v", err)
			}
			if err := g.Wait(); err != nil {
				t.Logf("grpc server error: %v", err)
			}
		},
	}
}

type simple struct {
	name string
	dc   string
}

func (s *simple) Flow(_ *testservice.Req, flow testservice.Simple_FlowServer) error {
	if err := flow.Send(&testservice.Resp{ServerName: "one", Datacenter: s.dc}); err != nil {
		return err
	}
	if err := flow.Send(&testservice.Resp{ServerName: "two", Datacenter: s.dc}); err != nil {
		return err
	}
	return nil
}

func (s *simple) Something(_ context.Context, _ *testservice.Req) (*testservice.Resp, error) {
	return &testservice.Resp{ServerName: s.name, Datacenter: s.dc}, nil
}

// fakeRPCListener mimics agent/consul.Server.listen to handle the RPCType byte.
// In the future we should be able to refactor Server and extract this RPC
// handling logic so that we don't need to use a fake.
// For now, since this logic is in agent/consul, we can't easily use Server.listen
// so we fake it.
type fakeRPCListener struct {
	t       *testing.T
	handler *Handler
}

func (f *fakeRPCListener) listen(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		go f.handleConn(conn)
	}
}

func (f *fakeRPCListener) handleConn(conn net.Conn) {
	buf := make([]byte, 1)

	if _, err := conn.Read(buf); err != nil {
		if err != io.EOF {
			fmt.Println("ERROR", err.Error())
		}
		conn.Close()
		return
	}
	typ := pool.RPCType(buf[0])

	if typ == pool.RPCGRPC {
		f.handler.Handle(conn)
		return
	}

	fmt.Println("ERROR: unexpected byte", typ)
	conn.Close()
}

func withError(t *testing.T, f func() error) func() {
	return func() {
		if err := f(); err != nil {
			t.Logf(err.Error())
		}
	}
}
