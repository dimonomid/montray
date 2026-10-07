// Package version reports build metadata injected by the Makefile.
package version

import (
	"fmt"
	"runtime"
	"strings"
)

// These values are replaced at build time using linker flags from the
// Makefile. The defaults keep development builds useful when built directly
// with go build or go test.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

// FullDescription returns the version information shown by --version.
func FullDescription(product string) string {
	return fullDescription(product, "")
}

// FullDescriptionWithBuildMode includes the build mode on the Built by line.
func FullDescriptionWithBuildMode(product, buildMode string) string {
	return fullDescription(product, " ("+buildMode+")")
}

func fullDescription(product, buildModeSuffix string) string {
	var description strings.Builder
	description.WriteString(fmt.Sprintf("%s %s\n", product, version))
	description.WriteString(fmt.Sprintf("Commit: %s\n", commit))
	description.WriteString(fmt.Sprintf("Build time: %s\n", date))
	description.WriteString(fmt.Sprintf("Built by: %s%s\n", builtBy, buildModeSuffix))
	description.WriteString(fmt.Sprintf("GOOS: %s\n", runtime.GOOS))
	if cgoEnabled {
		description.WriteString("CGO: enabled\n")
	} else {
		description.WriteString("CGO: disabled\n")
	}
	description.WriteString("\nWritten by Dmitry Frank (https://dmitryfrank.com)\n")
	return description.String()
}
