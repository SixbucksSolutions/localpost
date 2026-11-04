package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const registrationBodyLimit = 64 << 10

type registration struct {
	ID        string `json:"id"`
	Port      int    `json:"port"`
	directory string
	listener  net.Listener
	server    *http.Server
}

type registry struct {
	mu            sync.Mutex
	closed        bool
	registrations map[string]*registration
	listen        func(string, string) (net.Listener, error)
}

func newRegistry() *registry {
	return &registry{registrations: make(map[string]*registration), listen: net.Listen}
}

func (r *registry) register(directory string) (*registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("registration service is stopping")
	}
	listener, err := r.listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("allocate mailbox listener: %w", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port < 1024 || address.Port > 65535 {
		// Failed allocation must release even an unusable listener.
		_ = listener.Close()
		return nil, errors.New("mailbox listener did not allocate an unprivileged TCP port")
	}
	id := rand.Text()
	for r.registrations[id] != nil {
		id = rand.Text()
	}
	mailbox := &registration{
		ID: id, Port: address.Port, directory: directory, listener: listener,
		server: &http.Server{Handler: newMessageHandler(directory), ReadHeaderTimeout: 5 * time.Second},
	}
	r.registrations[id] = mailbox
	go r.serveMailbox(mailbox)
	return mailbox, nil
}

func (r *registry) serveMailbox(mailbox *registration) {
	// Shutdown and unexpected listener failure both relinquish this registration.
	_ = mailbox.server.Serve(mailbox.listener)
	_ = mailbox.server.Close()
	_ = mailbox.listener.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.registrations, mailbox.ID)
}

func (r *registry) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for id, mailbox := range r.registrations {
		// Closing both also covers shutdown before Serve starts in its goroutine.
		_ = mailbox.listener.Close()
		_ = mailbox.server.Close()
		delete(r.registrations, id)
	}
}

func validateDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("directory must be an absolute path")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return errors.New("directory must exist and be accessible")
	}
	if !info.IsDir() {
		return errors.New("directory must name an existing directory")
	}
	probe, err := os.CreateTemp(directory, ".localpost-probe-*")
	if err != nil {
		return errors.New("directory must be writable")
	}
	name := probe.Name()
	_, writeErr := probe.Write([]byte("localpost directory probe\n"))
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if writeErr != nil || closeErr != nil || removeErr != nil {
		return errors.New("directory must support writing and removing temporary files")
	}
	return nil
}

func (r *registry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/registrations" {
		writeAPIError(w, http.StatusNotFound, "not_found", "404 page not found")
		return
	}
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, registrationBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "registration body exceeds 64 KiB")
		} else {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "unable to read registration body")
		}
		return
	}
	var input *struct {
		Directory string `json:"directory"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "body must be one JSON object containing directory")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "body must contain exactly one JSON object")
		return
	}
	if err := validateDirectory(input.Directory); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_directory", err.Error())
		return
	}
	mailbox, err := r.register(input.Directory)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mailbox allocation unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// A lost response does not undo an established registration.
	_ = json.NewEncoder(w).Encode(mailbox)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	response := struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{}
	response.Error.Code = code
	response.Error.Message = message
	// Errors on the response stream cannot change the operation outcome.
	_ = json.NewEncoder(w).Encode(response)
}
