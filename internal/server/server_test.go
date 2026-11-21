package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/openapi"
	"github.com/mattchris3/writgate/internal/tools"
	"github.com/mattchris3/writgate/internal/upstream"
)

type fixture struct {
	session *mcp.ClientSession
	hits    *atomic.Int32
}

func start(t *testing.T, decide DecideFunc) fixture {
	t.Helper()
	doc, err := openapi.LoadFile("../../examples/payments-api/openapi.json", openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, _, err := tools.Build(doc)
	if err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"has_more":false}`)
	}))
	t.Cleanup(api.Close)
	client, err := upstream.New(api.URL, upstream.Options{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{Name: "writgate", Version: "test", Tools: ts, Client: client, Decide: decide})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return fixture{session: cs, hits: &hits}
}

func listed(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func TestDefaultHidesWrites(t *testing.T) {
	f := start(t, ReadOnlyOnly)
	got := listed(t, f.session)
	want := []string{"get_charge", "list_charges", "list_refunds"}
	if len(got) != len(want) {
		t.Fatalf("tools/list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools/list = %v, want %v", got, want)
		}
	}
}

func TestForcedCallIsDeniedWithoutUpstreamRequest(t *testing.T) {
	f := start(t, ReadOnlyOnly)
	res, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "cancel_refund",
		Arguments: map[string]any{"refund_id": "re_1"},
	})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !res.IsError || res.Content[0].(*mcp.TextContent).Text != "Denied by writgate policy: cancel_refund is not allowed" {
		t.Errorf("result = %+v", res)
	}
	if f.hits.Load() != 0 {
		t.Error("a denied call reached the upstream API")
	}
}

func TestAllowedCallReachesUpstream(t *testing.T) {
	f := start(t, ReadOnlyOnly)
	res, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_charges",
		Arguments: map[string]any{"limit": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if f.hits.Load() != 1 {
		t.Errorf("upstream hits = %d, want 1", f.hits.Load())
	}
}

func TestUnknownToolStaysAProtocolError(t *testing.T) {
	f := start(t, ReadOnlyOnly)
	if _, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"}); err == nil {
		t.Error("calling a tool that doesn't exist should be a protocol error, not a denial")
	}
}

func TestAllowAll(t *testing.T) {
	f := start(t, func(*tools.Tool) Decision { return Decision{Allowed: true, Rule: "allow *"} })
	if got := listed(t, f.session); len(got) != 6 {
		t.Errorf("tools/list = %v, want all 6", got)
	}
}

func TestNewRequiresClientAndDecide(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New with no client or decider succeeded")
	}
}
