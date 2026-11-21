package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/openapi"
	"github.com/mattchris3/writgate/internal/tools"
)

const apiKey = "sk_test_secret"

// The real payments API spec, so these tests track the demo target.
func paymentTools(t *testing.T) map[string]*tools.Tool {
	t.Helper()
	doc, err := openapi.LoadFile("../../examples/payments-api/openapi.json", openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, _, err := tools.Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*tools.Tool{}
	for _, tl := range ts {
		m[tl.Name()] = tl
	}
	return m
}

type seen struct {
	method, path, rawQuery string
	header                 http.Header
	body                   string
}

// recorder is a fake upstream that records each request and replies with
// a fixed status and body.
type recorder struct {
	mu     sync.Mutex
	calls  []seen
	status int
	body   string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.calls = append(r.calls, seen{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Clone(), string(b)})
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, _ = io.WriteString(w, r.body)
}

func setup(t *testing.T, status int, body string, basePath string) (*recorder, *Client) {
	t.Helper()
	rec := &recorder{status: status, body: body}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+basePath, Options{APIKey: apiKey, UserAgent: "writgate/test"})
	if err != nil {
		t.Fatal(err)
	}
	return rec, c
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("got %d content items", len(r.Content))
	}
	return r.Content[0].(*mcp.TextContent).Text
}

func TestCreateRefundRequest(t *testing.T) {
	ts := paymentTools(t)
	rec, c := setup(t, 201, `{"id":"re_1","status":"pending"}`, "")

	res := c.Call(context.Background(), ts["create_refund"], json.RawMessage(
		`{"charge_id":"ch_a/b","amount":12345678901234567,"reason":"duplicate","Idempotency-Key":"k-1"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", text(t, res))
	}
	if len(rec.calls) != 1 {
		t.Fatalf("upstream saw %d calls", len(rec.calls))
	}
	got := rec.calls[0]
	if got.method != "POST" || got.path != "/charges/ch_a%2Fb/refunds" {
		t.Errorf("call = %s %s", got.method, got.path)
	}
	if got.header.Get("Authorization") != "Bearer "+apiKey {
		t.Errorf("Authorization = %q", got.header.Get("Authorization"))
	}
	if got.header.Get("Idempotency-Key") != "k-1" || got.header.Get("User-Agent") != "writgate/test" {
		t.Errorf("headers = %v", got.header)
	}
	// Body fields only, with the large integer kept exact.
	want := `{"amount":12345678901234567,"reason":"duplicate"}`
	if got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}
	if res.StructuredContent == nil {
		t.Error("expected structuredContent, since the tool has an output schema")
	}
}

func TestQueryAndBasePath(t *testing.T) {
	ts := paymentTools(t)
	rec, c := setup(t, 200, `{"data":[],"has_more":false}`, "/v1/")

	res := c.Call(context.Background(), ts["list_charges"], json.RawMessage(`{"customer_id":"cus 42","limit":5}`))
	if res.IsError {
		t.Fatal(text(t, res))
	}
	got := rec.calls[0]
	if got.path != "/v1/charges" {
		t.Errorf("path = %s", got.path)
	}
	if got.rawQuery != "customer_id=cus+42&limit=5" {
		t.Errorf("query = %s", got.rawQuery)
	}
	if got.body != "" || got.header.Get("Content-Type") != "" {
		t.Errorf("GET sent a body: %q", got.body)
	}
}

func TestInvalidArgumentsAreNotSent(t *testing.T) {
	ts := paymentTools(t)
	rec, c := setup(t, 201, `{}`, "")
	cases := map[string]string{
		"not an object":        `[1,2]`,
		"missing required":     `{"amount":100}`,
		"wrong type":           `{"amount":"100","customer_id":"cus_1","Idempotency-Key":"k"}`,
		"smuggled auth header": `{"amount":100,"customer_id":"cus_1","Idempotency-Key":"k","Authorization":"Bearer x"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			res := c.Call(context.Background(), ts["create_charge"], json.RawMessage(args))
			if !res.IsError || !strings.HasPrefix(text(t, res), "Invalid arguments, request not sent") {
				t.Errorf("result = %+v", res)
			}
		})
	}
	if len(rec.calls) != 0 {
		t.Errorf("upstream saw %d calls, want none", len(rec.calls))
	}
}

func TestResults(t *testing.T) {
	ts := paymentTools(t)
	tests := []struct {
		name     string
		tool     string
		args     string
		status   int
		body     string
		isError  bool
		wantText string
	}{
		{"204", "cancel_refund", `{"refund_id":"re_1"}`, 204, "", false,
			"HTTP 204 No Content from DELETE /refunds/re_1 (no body)"},
		{"404", "get_charge", `{"charge_id":"ch_x"}`, 404, `{"detail":"No charge with id ch_x."}`, true,
			`HTTP 404 Not Found from GET /charges/ch_x: {"detail":"No charge with id ch_x."}`},
		{"409", "create_refund", `{"charge_id":"ch_1","Idempotency-Key":"k"}`, 409, `{"detail":"too much"}`, true,
			`HTTP 409 Conflict from POST /charges/ch_1/refunds: {"detail":"too much"}`},
		{"422 flattened", "list_charges", `{}`, 422,
			`{"detail":[{"loc":["query","limit"],"msg":"too small","type":"x"},{"loc":["query","status"],"msg":"bad","type":"y"}]}`, true,
			"HTTP 422 Unprocessable Entity from GET /charges: query.limit: too small; query.status: bad"},
		{"500 no body", "list_charges", `{}`, 500, "", true,
			"HTTP 500 Internal Server Error from GET /charges"},
		{"redirect not followed", "list_charges", `{}`, 302, "", true,
			"HTTP 302 Found from GET /charges"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := setup(t, tt.status, tt.body, "")
			res := c.Call(context.Background(), ts[tt.tool], json.RawMessage(tt.args))
			if res.IsError != tt.isError || text(t, res) != tt.wantText {
				t.Errorf("got isError=%v %q\nwant isError=%v %q", res.IsError, text(t, res), tt.isError, tt.wantText)
			}
		})
	}
}

func TestUnreachable(t *testing.T) {
	ts := paymentTools(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	c, err := New(url, Options{APIKey: apiKey})
	if err != nil {
		t.Fatal(err)
	}
	res := c.Call(context.Background(), ts["create_refund"], json.RawMessage(`{"charge_id":"ch_1","Idempotency-Key":"k"}`))
	msg := text(t, res)
	if !res.IsError || !strings.HasPrefix(msg, "Upstream unreachable for POST /charges/ch_1/refunds") {
		t.Errorf("result = %q", msg)
	}
	if strings.Contains(msg, apiKey) {
		t.Error("the API key leaked into the error")
	}
}

func TestPreparedHoldsNoCredentials(t *testing.T) {
	ts := paymentTools(t)
	_, c := setup(t, 201, `{}`, "")
	p, errRes := c.Prepare(ts["create_charge"], json.RawMessage(`{"amount":100,"customer_id":"cus_1","Idempotency-Key":"k-9"}`))
	if errRes != nil {
		t.Fatal(text(t, errRes))
	}
	if p.IdempotencyKey != "k-9" {
		t.Errorf("IdempotencyKey = %q", p.IdempotencyKey)
	}
	b, _ := json.Marshal(p.Header)
	if strings.Contains(string(b)+p.URL, apiKey) || p.Header.Get("Authorization") != "" {
		t.Errorf("prepared request carries credentials: %s %s", p.URL, b)
	}
}

func TestNewRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "localhost:8000", "ftp://x", "https://user:pass@api.example.com"} {
		if _, err := New(u, Options{}); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}
