package openapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLoadVersions(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr error
	}{
		{"3.0", "openapi: 3.0.3\ninfo: {title: t, version: \"1\"}\npaths: {}\n", nil},
		{"3.1", "openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths: {}\n", nil},
		{"swagger 2.0", "swagger: \"2.0\"\ninfo: {title: t, version: \"1\"}\npaths: {}\n", ErrSwagger2},
		{"3.2", "openapi: 3.2.0\ninfo: {title: t, version: \"1\"}\npaths: {}\n", ErrUnsupportedVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.spec), Options{})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSwaggerMessage(t *testing.T) {
	_, err := Load([]byte("swagger: \"2.0\"\ninfo: {title: t, version: \"1\"}\npaths: {}\n"), Options{})
	if err == nil || !strings.Contains(err.Error(), "convert the spec to OpenAPI 3.x first") {
		t.Errorf("err = %v", err)
	}
}

func TestNotOpenAPI(t *testing.T) {
	if _, err := Load([]byte("hello: world\n"), Options{}); err == nil {
		t.Error("expected an error for a document that isn't OpenAPI")
	}
}

// TestRemoteRefsOffByDefault checks that loading a spec never fetches a
// remote $ref unless AllowRemoteRefs is set.
func TestRemoteRefsOffByDefault(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type": "string"}`))
	}))
	defer srv.Close()

	spec := `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /a:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "` + srv.URL + `/schema.json"}
`
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := Load([]byte(spec), Options{Logger: quiet}); err == nil {
		t.Error("an unresolvable remote $ref should be an error")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("remote $ref fetched %d times with AllowRemoteRefs off", n)
	}

	// And the same spec does fetch it once the flag is on, so the check
	// above can't pass by accident.
	if _, err := Load([]byte(spec), Options{AllowRemoteRefs: true, Logger: quiet}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Error("remote $ref not fetched with AllowRemoteRefs on")
	}
}

func TestLoadFile(t *testing.T) {
	if _, err := LoadFile("testdata/missing.yaml", Options{}); err == nil {
		t.Error("expected an error for a missing file")
	}
}
