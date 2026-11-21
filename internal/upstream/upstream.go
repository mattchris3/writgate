// Package upstream turns a tool call into an HTTP request to the API behind
// writgate, and the HTTP response back into an MCP tool result
// (docs/SPEC.md section 3).
//
// A call happens in three steps so the audit log can sit between them:
// Prepare validates the arguments and builds the request without sending
// it (the audit "intent" is written from it), Send performs it, and Result
// maps the response (the audit "outcome" is written alongside).
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/tools"
)

// MaxResponseBytes caps how much of an upstream response is read.
const MaxResponseBytes = 10 << 20

// IdempotencyHeader is the header recovery uses to find a call's result.
const IdempotencyHeader = "Idempotency-Key"

// Client sends tool calls to one upstream API.
type Client struct {
	base   *url.URL
	apiKey string
	http   *http.Client
	agent  string
}

// Options configures a Client.
type Options struct {
	// APIKey is sent as "Authorization: Bearer <key>". It comes from
	// writgate's environment and never from the agent.
	APIKey string
	// Timeout bounds each upstream request. Zero means 30 seconds.
	Timeout time.Duration
	// UserAgent is sent with every request.
	UserAgent string
}

// New returns a Client for the API at baseURL, e.g. "https://api.example.com/v1".
func New(baseURL string, opts Options) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("upstream URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("upstream URL %q must be an absolute http(s) URL", baseURL)
	}
	if u.User != nil {
		return nil, errors.New("upstream URL must not contain credentials; set the API key in the environment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		base:   u,
		apiKey: opts.APIKey,
		agent:  opts.UserAgent,
		http: &http.Client{
			Timeout: timeout,
			// Never follow redirects: a redirect could send the call, or the
			// key, somewhere the operator didn't configure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Prepared is a validated request that has not been sent. It holds no
// credentials, so it is safe to write to the audit log as it is.
type Prepared struct {
	Tool           *tools.Tool
	Args           map[string]any
	Method         string
	Path           string // the resolved path, e.g. "/charges/ch_1/refunds"
	URL            string // full URL, without credentials
	Header         http.Header
	Body           []byte // nil when there is no body
	IdempotencyKey string // empty when the call has none
}

// Call is the whole call for a server with no audit log: Prepare, Send, Result.
func (c *Client) Call(ctx context.Context, t *tools.Tool, rawArgs json.RawMessage) *mcp.CallToolResult {
	p, errRes := c.Prepare(t, rawArgs)
	if errRes != nil {
		return errRes
	}
	resp, err := c.Send(ctx, p)
	return Result(p, resp, err)
}

// Prepare validates the arguments against the tool's input schema and
// builds the request. On invalid arguments it returns an error result and
// nothing is sent.
func (c *Client) Prepare(t *tools.Tool, rawArgs json.RawMessage) (*Prepared, *mcp.CallToolResult) {
	// Decode twice: once into plain values for schema validation, and once
	// with json.Number so large integers (minor-unit amounts, ids) reach the
	// API digit for digit instead of through a float64.
	args, exact := map[string]any{}, map[string]any{}
	if trimmed := bytes.TrimSpace(rawArgs); len(trimmed) > 0 && string(trimmed) != "null" {
		if err := json.Unmarshal(trimmed, &args); err != nil {
			return nil, errorResult("Invalid arguments, request not sent: arguments must be a JSON object")
		}
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&exact); err != nil {
			return nil, errorResult("Invalid arguments, request not sent: arguments must be a JSON object")
		}
	}
	if err := t.Input.Validate(args); err != nil {
		return nil, errorResult("Invalid arguments, request not sent: %v", err)
	}

	path := t.Path
	query := url.Values{}
	header := http.Header{}
	fields := map[string]any{}
	var whole any
	hasBody := false
	for name, v := range exact {
		arg, ok := t.Args[name]
		if !ok {
			continue // the schema forbids extra properties, so this can't happen
		}
		switch arg.In {
		case tools.InPath:
			path = strings.ReplaceAll(path, "{"+arg.Name+"}", url.PathEscape(scalar(v)))
		case tools.InQuery:
			if list, ok := v.([]any); ok {
				for _, item := range list {
					query.Add(arg.Name, scalar(item))
				}
			} else {
				query.Set(arg.Name, scalar(v))
			}
		case tools.InHeader:
			header.Set(arg.Name, scalar(v))
		case tools.InBodyField:
			fields[arg.Name] = v
			hasBody = true
		case tools.InBody:
			whole = v
			hasBody = true
		}
	}

	var body []byte
	if hasBody {
		payload := any(fields)
		if whole != nil {
			payload = whole
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, errorResult("Invalid arguments, request not sent: %v", err)
		}
		body = b
		header.Set("Content-Type", "application/json")
	}
	header.Set("Accept", "application/json")

	// path is already escaped segment by segment, so join strings rather
	// than going through url.URL.Path, which would escape it again.
	full := c.base.String() + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	return &Prepared{
		Tool:           t,
		Args:           args,
		Method:         t.Method,
		Path:           path,
		URL:            full,
		Header:         header,
		Body:           body,
		IdempotencyKey: header.Get(IdempotencyHeader),
	}, nil
}

// Response is what came back from the upstream API.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool // the body was longer than MaxResponseBytes
	Duration  time.Duration
}

// Send performs a prepared request. An error means no response came back
// (the API was unreachable, the request timed out or was cancelled), so the
// call's result is unknown.
func (c *Client) Send(ctx context.Context, p *Prepared) (*Response, error) {
	var body io.Reader
	if p.Body != nil {
		body = bytes.NewReader(p.Body)
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header = p.Header.Clone()
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.agent != "" {
		req.Header.Set("User-Agent", c.agent)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	r := &Response{Status: resp.StatusCode, Header: resp.Header, Body: b, Duration: time.Since(start)}
	if len(b) > MaxResponseBytes {
		r.Body, r.Truncated = b[:MaxResponseBytes], true
	}
	return r, nil
}

// Result maps an upstream response, or the error from Send, to a tool result.
func Result(p *Prepared, resp *Response, sendErr error) *mcp.CallToolResult {
	call := p.Method + " " + p.Path
	if sendErr != nil {
		return errorResult("Upstream unreachable for %s: %v. The result is unknown; check before retrying, and reuse the same Idempotency-Key if you retry.", call, sendErr)
	}
	status := fmt.Sprintf("HTTP %d %s from %s", resp.Status, http.StatusText(resp.Status), call)

	if resp.Status < 200 || resp.Status > 299 {
		detail := strings.TrimSpace(string(resp.Body))
		if resp.Status == http.StatusUnprocessableEntity {
			if flat, ok := fastAPIDetail(resp.Body); ok {
				detail = flat
			}
		}
		if detail == "" {
			return errorResult("%s", status)
		}
		return errorResult("%s: %s", status, detail)
	}

	if len(bytes.TrimSpace(resp.Body)) == 0 {
		return textResult(status + " (no body)")
	}
	if resp.Truncated {
		return errorResult("%s: the response was larger than %d bytes and was cut off", status, MaxResponseBytes)
	}
	res := textResult(string(resp.Body))
	if p.Tool.Def.OutputSchema != nil {
		var structured any
		if json.Unmarshal(resp.Body, &structured) == nil {
			res.StructuredContent = structured
		}
	}
	return res
}

// fastAPIDetail flattens FastAPI's 422 body to "loc: msg; loc: msg".
func fastAPIDetail(b []byte) (string, bool) {
	var v struct {
		Detail []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		} `json:"detail"`
	}
	if json.Unmarshal(b, &v) != nil || len(v.Detail) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(v.Detail))
	for _, d := range v.Detail {
		loc := make([]string, 0, len(d.Loc))
		for _, l := range d.Loc {
			loc = append(loc, fmt.Sprint(l))
		}
		parts = append(parts, strings.Join(loc, ".")+": "+d.Msg)
	}
	return strings.Join(parts, "; "), true
}

// scalar formats one argument value for a path, query or header.
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(format string, a ...any) *mcp.CallToolResult {
	r := textResult(fmt.Sprintf(format, a...))
	r.IsError = true
	return r
}
