package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Keep faults at the filesystem boundary: reads, error responses, publication
// guards and cleanup still run through the real handler.
type faultMessageFile struct {
	*os.File
	write    func([]byte) (int, error)
	closeErr error
	closed   bool
	closes   *int
}

func (f *faultMessageFile) Write(data []byte) (int, error) {
	if f.write != nil {
		return f.write(data)
	}
	return f.File.Write(data)
}

func (f *faultMessageFile) Close() error {
	*f.closes++
	f.closed = true
	return errors.Join(f.File.Close(), f.closeErr)
}

type storageResponseRecorder struct{ *httptest.ResponseRecorder }

func (*storageResponseRecorder) SetReadDeadline(time.Time) error { return nil }

func TestMessageStorageFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stage        string
		failure      error
		partial      bool
		short        bool
		cleanupFails bool
		publishes    bool
	}{
		{name: "create permission", stage: "create", failure: os.ErrPermission},
		{name: "create disk full", stage: "create", failure: syscall.ENOSPC},
		{name: "write permission", stage: "write", failure: os.ErrPermission},
		{name: "write disk full", stage: "write", failure: syscall.ENOSPC},
		{name: "partial disk full", stage: "write", failure: syscall.ENOSPC, partial: true},
		{name: "short write", stage: "write", short: true},
		{name: "close failure", stage: "close", failure: syscall.ENOSPC},
		{name: "publish permission", stage: "publish", failure: os.ErrPermission},
		{name: "publish disk full", stage: "publish", failure: syscall.ENOSPC},
		{name: "publish unsupported", stage: "publish", failure: errors.New("hard links unsupported")},
		{name: "publish collision", stage: "collision"},
		{name: "failed upload cleanup failure", stage: "write", failure: syscall.ENOSPC, partial: true, cleanupFails: true},
		{name: "published cleanup failure", cleanupFails: true, publishes: true},
		{name: "publish uncertain outcome", stage: "publish", failure: os.ErrPermission, publishes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			handler := newMessageHandler(directory)
			const finalName = "localpost_new.dat"
			const oldName = "localpost_existing.dat"
			handler.filename = func() string { return finalName }
			original := []byte{0, 255, 'o', 'l', 'd', '\n'}
			if err := os.WriteFile(filepath.Join(directory, oldName), original, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.stage == "collision" {
				handler.filename = func() string { return oldName }
			}
			payload := []byte{0, 255, 'n', 'e', 'w', '\r', '\n'}
			creates, writes, closes, links, removes := 0, 0, 0, 0, 0
			var temporary string
			var file *faultMessageFile
			handler.storage.createTemp = func(dir, pattern string) (messageFile, error) {
				creates++
				if tc.stage == "create" {
					return nil, tc.failure
				}
				actual, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				temporary = actual.Name()
				file = &faultMessageFile{File: actual, closes: &closes}
				if tc.stage == "close" {
					file.closeErr = tc.failure
				}
				file.write = func(data []byte) (int, error) {
					writes++
					if tc.stage != "write" {
						return actual.Write(data)
					}
					if tc.partial || tc.short {
						count, err := actual.Write(data[:len(data)-1])
						if err != nil {
							return count, err
						}
						return count, tc.failure
					}
					return 0, tc.failure
				}
				return file, nil
			}
			handler.storage.link = func(source, target string) error {
				links++
				if file == nil || !file.closed {
					t.Error("published before closing temporary file")
				}
				actual, err := os.ReadFile(source)
				if err != nil || !bytes.Equal(actual, payload) {
					t.Errorf("publication source differs: %v", err)
				}
				if tc.stage == "publish" && !tc.publishes {
					return tc.failure
				}
				if err := os.Link(source, target); err != nil {
					return err
				}
				// An error does not prove publication had no side effect. Preserve any
				// resulting final file rather than deleting it or retrying publication.
				return tc.failure
			}
			handler.storage.remove = func(name string) error {
				removes++
				if file == nil || !file.closed {
					t.Error("removed temporary file before closing it")
				}
				if name != temporary {
					t.Errorf("cleanup targets %q, want temporary %q", name, temporary)
				}
				if tc.cleanupFails {
					return os.ErrPermission
				}
				return os.Remove(name)
			}
			request := httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/octet-stream")
			response := &storageResponseRecorder{httptest.NewRecorder()}
			handler.ServeHTTP(response, request)
			expectedStatus := http.StatusInternalServerError
			if tc.stage == "" {
				expectedStatus = http.StatusOK
			}
			if response.Code != expectedStatus || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response status/type = %d %q", response.Code, response.Header().Get("Content-Type"))
			}
			if expectedStatus != http.StatusOK {
				var envelope struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error.Code != "delivery_failed" || envelope.Error.Message == "" {
					t.Fatalf("invalid error envelope: %+v", envelope)
				}
			} else {
				var receipt messageReceipt
				if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Filename != finalName || receipt.Bytes != int64(len(payload)) {
					t.Fatalf("invalid receipt: %+v", receipt)
				}
			}
			expectedWrites, expectedCloses, expectedRemoves := 1, 1, 1
			if tc.stage == "create" {
				expectedWrites, expectedCloses, expectedRemoves = 0, 0, 0
			}
			expectedLinks := 0
			if tc.stage == "" || tc.stage == "publish" || tc.stage == "collision" {
				expectedLinks = 1
			}
			if creates != 1 || writes != expectedWrites || closes != expectedCloses || links != expectedLinks || removes != expectedRemoves {
				t.Fatalf("operation counts create/write/close/link/remove = %d/%d/%d/%d/%d", creates, writes, closes, links, removes)
			}
			preserved, err := os.ReadFile(filepath.Join(directory, oldName))
			if err != nil || !bytes.Equal(preserved, original) {
				t.Fatalf("existing final changed: %v", err)
			}
			result, err := os.ReadFile(filepath.Join(directory, finalName))
			if tc.publishes {
				if err != nil || !bytes.Equal(result, payload) {
					t.Fatalf("published bytes changed or erased: %v", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed upload left final file: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			expectedEntries := 1
			if tc.publishes {
				expectedEntries++
			}
			if tc.cleanupFails {
				expectedEntries++
			}
			if len(entries) != expectedEntries {
				t.Fatalf("directory has %d entries, want %d", len(entries), expectedEntries)
			}
			if tc.cleanupFails {
				remaining, err := os.ReadFile(temporary)
				if err != nil {
					t.Fatal(err)
				}
				expected := payload
				if tc.partial {
					expected = payload[:len(payload)-1]
				}
				if !bytes.Equal(remaining, expected) {
					t.Fatal("retained temporary bytes differ")
				}
			}
		})
	}
}

// Check the error chain as well as HTTP classification so disk-full and short
// writes stay recognizable to internal callers without exposing OS details.
func TestMessageStorageWriteErrorCauses(t *testing.T) {
	for _, failure := range []error{syscall.ENOSPC, io.ErrShortWrite} {
		writer := storageErrorWriter{failure: failure}
		_, err := copyMessage(writer, bytes.NewReader([]byte("payload")), http.NewResponseController(&storageResponseRecorder{httptest.NewRecorder()}), time.Second)
		if !errors.Is(err, errMessageStorage) || !errors.Is(err, failure) {
			t.Fatalf("storage error lost cause: %v", err)
		}
	}
}

type storageErrorWriter struct{ failure error }

func (w storageErrorWriter) Write([]byte) (int, error) { return 0, w.failure }
