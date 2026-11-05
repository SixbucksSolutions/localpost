package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Root-relative removal prevents stale symbolic links or junctions from
// escaping the configured root during startup cleanup.
func clearMailboxRoot(directory string, output io.Writer) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("cannot open directory for startup cleanup")
	}
	defer func() { _ = root.Close() }()
	entries, err := root.Open(".")
	if err != nil {
		return errors.New("cannot read directory for startup cleanup")
	}
	defer func() { _ = entries.Close() }()
	warned := false
	for {
		batch, readErr := entries.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errors.New("cannot read directory for startup cleanup")
		}
		if len(batch) > 0 && !warned {
			// Diagnostics never include retained filenames or mailbox contents.
			_, _ = fmt.Fprintln(output, "localpost: WARNING: existing mailbox root contents will be deleted before startup")
			warned = true
		}
		for _, entry := range batch {
			if err := root.RemoveAll(entry.Name()); err != nil {
				return errors.New("cannot remove existing directory contents during startup cleanup")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	// Reopen the directory to verify deletion did not leave entries behind.
	remaining, err := root.Open(".")
	if err != nil {
		return errors.New("cannot verify directory after startup cleanup")
	}
	defer func() { _ = remaining.Close() }()
	batch, err := remaining.ReadDir(1)
	if len(batch) != 0 || !errors.Is(err, io.EOF) {
		return errors.New("directory is not empty after startup cleanup")
	}
	return nil
}
