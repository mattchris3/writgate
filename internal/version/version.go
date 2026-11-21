// Package version holds the writgate build version.
package version

// Version is set at build time with:
//
//	go build -ldflags "-X github.com/mattchris3/writgate/internal/version.Version=v0.1.0"
//
// A plain `go install` of a tagged release reports the module version instead.
var Version = "dev"
