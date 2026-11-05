package main

import (
	"fmt"
	"io"
	"runtime"
)

// Release builds set these values with -ldflags -X main.releaseVersion=...
// and -X main.sourceCommit=...; ordinary builds retain their development identity.
var (
	releaseVersion = "dev"
	sourceCommit   = "unknown"
)

func writeVersion(output io.Writer) error {
	_, err := fmt.Fprintf(output, "localpost %s (commit %s; %s; %s/%s)\n",
		releaseVersion, sourceCommit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}
