// Package openapi loads an OpenAPI document and checks that writgate can
// serve it: OpenAPI 3.0.x or 3.1.x, with remote $refs off unless asked for.
package openapi

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/utils"
)

// Options controls how a spec is loaded.
type Options struct {
	// BasePath is the directory that relative file $refs resolve against.
	// Empty means file $refs are not followed.
	BasePath string
	// AllowRemoteRefs lets the loader fetch $refs over HTTP(S). Off by
	// default, so loading a spec never makes a network call on its own.
	AllowRemoteRefs bool
	// Logger receives libopenapi's warnings. Nil means stderr. It must never
	// be stdout, which is the MCP channel in stdio mode.
	Logger *slog.Logger
}

// ErrSwagger2 is returned for a Swagger 2.0 document.
var ErrSwagger2 = errors.New("swagger 2.0 is not supported: convert the spec to OpenAPI 3.x first")

// ErrUnsupportedVersion is returned for an OpenAPI version other than 3.0 or 3.1.
var ErrUnsupportedVersion = errors.New("unsupported OpenAPI version: writgate accepts 3.0.x and 3.1.x")

// LoadFile reads a spec from disk. Relative file $refs resolve against the
// spec's own directory.
func LoadFile(path string, opts Options) (*v3.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	if opts.BasePath == "" {
		opts.BasePath = filepath.Dir(path)
	}
	return Load(data, opts)
}

// Load parses a spec held in memory.
func Load(data []byte, opts Options) (*v3.Document, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	cfg := &datamodel.DocumentConfiguration{
		Logger:                logger,
		BasePath:              opts.BasePath,
		AllowFileReferences:   opts.BasePath != "",
		AllowRemoteReferences: opts.AllowRemoteRefs,
	}
	doc, err := libopenapi.NewDocumentWithConfiguration(data, cfg)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	info := doc.GetSpecInfo()
	switch {
	case info.SpecType == utils.OpenApi2:
		return nil, ErrSwagger2
	case info.SpecType != utils.OpenApi3:
		return nil, fmt.Errorf("parse spec: not an OpenAPI document")
	case info.SpecFormat != datamodel.OAS3 && info.SpecFormat != datamodel.OAS31:
		return nil, fmt.Errorf("%w (got %s)", ErrUnsupportedVersion, info.Version)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("build spec model: %w", err)
	}
	if model == nil {
		return nil, errors.New("build spec model: no model produced")
	}
	return &model.Model, nil
}
