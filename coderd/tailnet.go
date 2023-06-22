package coderd

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/xerrors"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"tailscale.com/derp"
	"tailscale.com/tailcfg"

	"cdr.dev/slog"
	"github.com/coder/coder/coderd/wsconncache"
	"github.com/coder/coder/codersdk"
	"github.com/coder/coder/tailnet"
)

var defaultTransport *http.Transport

func init() {
	var valid bool
	defaultTransport, valid = http.DefaultTransport.(*http.Transport)
	if !valid {
		panic("dev error: default transport is the wrong type")
	}
}

func newServerTailnet(
	ctx context.Context,
	logger slog.Logger,
	derpServer *derp.Server,
	derpMap *tailcfg.DERPMap,
	coord *atomic.Pointer[tailnet.Coordinator],
	cache *wsconncache.Cache,
) *serverTailnet {
	conn, err := tailnet.NewConn(&tailnet.Options{
		Addresses: []netip.Prefix{netip.PrefixFrom(tailnet.IP(), 128)},
		DERPMap:   derpMap,
		Logger:    logger.Named("tailnet"),
	})
	if err != nil {
		panic(xerrors.Errorf("create tailnet: %w", err))
	}

	tn := &serverTailnet{
		logger:      logger,
		conn:        conn,
		coordinator: coord,
		cache:       cache,
		agentNodes:  map[uuid.UUID]*tailnetNode{},
		transport:   defaultTransport.Clone(),
	}
	tn.transport.DialContext = tn.dialContext
	tn.transport.MaxIdleConnsPerHost = 10
	tn.transport.MaxIdleConns = 0

	conn.SetNodeCallback(func(node *tailnet.Node) {
		tn.nodesMu.Lock()
		ids := make([]uuid.UUID, 0, len(tn.agentNodes))
		for id := range tn.agentNodes {
			ids = append(ids, id)
		}
		tn.nodesMu.Unlock()

		err := (*tn.coordinator.Load()).BroadcastToAgents(ids, node)
		if err != nil {
			tn.logger.Error(context.Background(), "broadcast server node to agents", slog.Error(err))
		}
	})

	// This is set to allow local DERP traffic to be proxied through memory
	// instead of needing to hit the external access URL. Don't use the ctx
	// given in this callback, it's only valid while connecting.
	conn.SetDERPRegionDialer(func(_ context.Context, region *tailcfg.DERPRegion) net.Conn {
		if !region.EmbeddedRelay {
			return nil
		}
		left, right := net.Pipe()
		go func() {
			defer left.Close()
			defer right.Close()
			brw := bufio.NewReadWriter(bufio.NewReader(right), bufio.NewWriter(right))
			derpServer.Accept(ctx, right, brw, "internal")
		}()
		return left
	})

	return tn
}

type tailnetNode struct {
	node           *tailnet.Node
	lastConnection time.Time
	stop           func()
}

type serverTailnet struct {
	logger      slog.Logger
	conn        *tailnet.Conn
	coordinator *atomic.Pointer[tailnet.Coordinator]
	cache       *wsconncache.Cache
	nodesMu     sync.Mutex
	// agentNodes is a map of agent tailnetNodes the server wants to keep a
	// connection to.
	agentNodes map[uuid.UUID]*tailnetNode

	transport *http.Transport
}

func (s *serverTailnet) updateNode(id uuid.UUID, node *tailnet.Node) {
	s.nodesMu.Lock()
	cached, ok := s.agentNodes[id]
	if ok {
		cached.node = node
	}
	s.nodesMu.Unlock()

	if ok {
		err := s.conn.UpdateNodes([]*tailnet.Node{node}, false)
		if err != nil {
			s.logger.Error(context.Background(), "update node ..................... t", slog.Error(err))
			return
		}
	}
}

// func (s *serverTailnet) gatherNodes() []*tailnet.Node {
// 	nodes := make([]*tailnet.Node, 0, len(s.agentNodes))
// 	for _, node := range s.agentNodes {
// 		nodes = append(nodes, node.node)
// 	}

// 	return nodes
// }

func (s *serverTailnet) ReverseProxy(targetURL, dashboardURL *url.URL, agentID uuid.UUID) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "ERROR HAPPENED", err.Error())
		// site.RenderStaticErrorPage(w, r, site.ErrorPageData{
		// 	Status:       http.StatusBadGateway,
		// 	Title:        "Bad Gateway",
		// 	Description:  "Failed to proxy request to application: " + err.Error(),
		// 	RetryEnabled: true,
		// 	DashboardURL: dashboardURL.String(),
		// })
	}
	proxy.Director = s.director(agentID, proxy.Director)
	proxy.Transport = s.transport

	return proxy
}

type agentIDKey struct{}

func (*serverTailnet) director(agentID uuid.UUID, prev func(req *http.Request)) func(req *http.Request) {
	return func(req *http.Request) {
		ctx := context.WithValue(req.Context(), agentIDKey{}, agentID)
		*req = *req.WithContext(ctx)
		prev(req)
	}
}

func (s *serverTailnet) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	agentID, ok := ctx.Value(agentIDKey{}).(uuid.UUID)
	if !ok {
		return nil, xerrors.Errorf("no agent id attached")
	}

	return s.DialAgentNetConn(ctx, agentID, network, addr)
}

func (s *serverTailnet) getNode(agentID uuid.UUID) (*tailnet.Node, error) {
	s.nodesMu.Lock()
	node, ok := s.agentNodes[agentID]
	// If we don't have the node, fetch it from the coordinator.
	if !ok {
		coord := *s.coordinator.Load()
		_node := coord.Node(agentID)
		// The coordinator doesn't have the node either. Nothing we can do here.
		if _node == nil {
			s.nodesMu.Unlock()
			return nil, xerrors.Errorf("node %q not found; total %d nodes", agentID.String())
		}
		stop := coord.SubscribeAgent(agentID, s.updateNode)
		node = &tailnetNode{
			node:           _node,
			lastConnection: time.Now(),
			stop:           stop,
		}
		s.agentNodes[agentID] = node

		_ = coord.BroadcastToAgents([]uuid.UUID{agentID}, s.conn.Node())
	}
	s.nodesMu.Unlock()

	if len(node.node.Addresses) == 0 {
		return nil, xerrors.New("agent has no reachable addresses")
	}

	// if we didn't already have the node locally, add it to our tailnet.
	if !ok {
		err := s.conn.UpdateNodes([]*tailnet.Node{node.node}, false)
		if err != nil {
			return nil, xerrors.Errorf("set nodes: %w", err)
		}
	}

	return node.node, nil
}

func (s *serverTailnet) AgentConn(_ context.Context, agentID uuid.UUID) (*codersdk.WorkspaceAgentConn, error) {
	return codersdk.NewWorkspaceAgentConn(s.conn, codersdk.WorkspaceAgentConnOptions{
		AgentID: agentID,
		GetNode: s.getNode,
		// TODO: close ticket
		CloseFunc: func() {},
	}), nil
}

type dialer interface {
	DialContextTCP(ctx context.Context, ipp netip.AddrPort) (*gonet.TCPConn, error)
	DialContextUDP(ctx context.Context, ipp netip.AddrPort) (*gonet.UDPConn, error)
}

func (s *serverTailnet) DialAgentNetConn(ctx context.Context, agentID uuid.UUID, network, addr string) (net.Conn, error) {
	node, err := s.getNode(agentID)
	if err != nil {
		return nil, err
	}

	var dial dialer = s.conn

	if node.Addresses[0].Addr() == codersdk.WorkspaceAgentIP {
		conn, release, err := s.cache.Acquire(agentID)
		if err != nil {
			return nil, xerrors.Errorf("acquire conn from legacy cache: %w", err)
		}
		defer release()

		dial = conn
	}

	_, rawPort, _ := net.SplitHostPort(addr)
	port, _ := strconv.ParseUint(rawPort, 10, 16)
	ipp := netip.AddrPortFrom(node.Addresses[0].Addr(), uint16(port))

	if network == "tcp" {
		return dial.DialContextTCP(ctx, ipp)
	} else if network == "udp" {
		return dial.DialContextUDP(ctx, ipp)
	} else {
		return nil, xerrors.Errorf("unknown network %q", network)
	}
}

func (s *serverTailnet) Close() error {
	s.conn.Close()
	s.transport.CloseIdleConnections()
	return nil
}
