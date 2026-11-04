package main

import (
	"bytes"
	"context"
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
	"strings"
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

	mu      *sync.Mutex
	closing bool
	forced  bool
	active  int
	drained chan struct{}
	done    chan struct{}
}

type registry struct {
	mu                sync.Mutex
	gracePeriod       time.Duration
	registrationLimit int
	uploads           *uploadSlots
	closed            bool
	registrations     map[string]*registration
	listen            func(string, string) (net.Listener, error)
}

func newRegistry() *registry {
	return &registry{registrations: make(map[string]*registration), listen: net.Listen, gracePeriod: 30 * time.Second, registrationLimit: defaultRegistrationLimit, uploads: newUploadSlots(defaultUploadLimit)}
}

func (r *registry) register(directory string) (*registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("registration service is stopping")
	}
	if len(r.registrations) >= r.registrationLimit {
		return nil, errRegistrationLimit
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
		mu: &sync.Mutex{}, drained: make(chan struct{}), done: make(chan struct{}),
	}
	handler := newMessageHandler(directory)
	handler.mailbox = mailbox
	handler.uploads = r.uploads
	mailbox.server = &http.Server{Handler: withCORS(handler, "POST, OPTIONS"), ReadHeaderTimeout: 5 * time.Second}
	r.registrations[id] = mailbox
	go r.serveMailbox(mailbox)
	return mailbox, nil
}

// Admission and publication share this lock with the forced cutoff. A request
// already admitted may finish during draining, but cannot publish after force.
func (m *registration) admit() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return false
	}
	m.active++
	return true
}

func (m *registration) finished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if m.closing && m.active == 0 {
		close(m.drained)
	}
}

func (m *registration) freeze() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closing = true
	if m.active == 0 {
		close(m.drained)
	}
}

func (m *registration) force() {
	m.mu.Lock()
	m.forced = true
	m.mu.Unlock()
	// Both closes are needed if Serve has not yet started.
	_ = m.listener.Close()
	_ = m.server.Close()
}

func (r *registry) serveMailbox(mailbox *registration) {
	_ = mailbox.server.Serve(mailbox.listener)
	r.mu.Lock()
	defer r.mu.Unlock()
	mailbox.mu.Lock()
	closing := mailbox.closing
	mailbox.mu.Unlock()
	if !closing {
		// An unexpected listener failure aborts existing connections. An
		// intentional Shutdown also returns from Serve, but must keep draining.
		r.startStop(mailbox)
		mailbox.force()
	}
}

// startStop is called under r.mu. The mapping remains owned until finalization,
// so simultaneous DELETE requests join the same operation.
func (r *registry) startStop(mailbox *registration) {
	mailbox.mu.Lock()
	closing := mailbox.closing
	mailbox.mu.Unlock()
	if closing {
		return
	}
	mailbox.freeze()
	go r.drain(mailbox)
}

func (r *registry) drain(mailbox *registration) {
	ctx, cancel := context.WithTimeout(context.Background(), r.gracePeriod)
	defer cancel()
	// Let Shutdown own the tracked listener close. Closing it first can make
	// Shutdown report net.ErrClosed while Serve is still removing its listener,
	// incorrectly marking an otherwise graceful drain as forced.
	if err := mailbox.server.Shutdown(ctx); err != nil {
		mailbox.force()
	}
	// Shutdown cannot track a listener before Serve starts. Release that
	// allocation too; the shutdown flag prevents a later Serve from using it.
	_ = mailbox.listener.Close()
	// Close interrupts body reads. Wait for their deferred temporary-file cleanup
	// before reporting completion or releasing ownership of the mapping.
	<-mailbox.drained
	r.mu.Lock()
	delete(r.registrations, mailbox.ID)
	close(mailbox.done)
	r.mu.Unlock()
}

func (r *registry) stopAll() []*registration {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	mailboxes := make([]*registration, 0, len(r.registrations))
	for _, mailbox := range r.registrations {
		r.startStop(mailbox)
		mailboxes = append(mailboxes, mailbox)
	}
	return mailboxes
}

func (r *registry) shutdown(ctx context.Context) {
	mailboxes := r.stopAll()
	// The shared deadline also caps DELETE operations already draining.
	stop := context.AfterFunc(ctx, func() {
		for _, mailbox := range mailboxes {
			select {
			case <-mailbox.done:
			default:
				mailbox.force()
			}
		}
	})
	defer stop()
	for _, mailbox := range mailboxes {
		<-mailbox.done
	}
}

// close is the immediate cleanup path for tests and unrecoverable failures.
func (r *registry) close() {
	mailboxes := r.stopAll()
	for _, mailbox := range mailboxes {
		mailbox.force()
	}
	for _, mailbox := range mailboxes {
		<-mailbox.done
	}
}

func (r *registry) unregister(w http.ResponseWriter, id string) {
	r.mu.Lock()
	mailbox := r.registrations[id]
	if mailbox != nil {
		r.startStop(mailbox)
	}
	r.mu.Unlock()
	if mailbox == nil {
		writeAPIError(w, http.StatusNotFound, "registration_not_found", "registration not found")
		return
	}
	<-mailbox.done
	mailbox.mu.Lock()
	forced := mailbox.forced
	mailbox.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	// Caller disconnection does not cancel the owned cleanup operation.
	_ = json.NewEncoder(w).Encode(struct {
		ID           string `json:"id"`
		Unregistered bool   `json:"unregistered"`
		Forced       bool   `json:"forced"`
	}{mailbox.ID, true, forced})
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
	controller := http.NewResponseController(w)
	if id, ok := strings.CutPrefix(request.URL.Path, "/registrations/"); ok && id != "" && !strings.Contains(id, "/") {
		if request.Method != http.MethodDelete {
			abortMessageBody(w, controller)
			w.Header().Set("Allow", http.MethodDelete)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be DELETE")
			return
		}
		abortMessageBody(w, controller)
		r.unregister(w, id)
		return
	}
	if request.URL.Path != "/registrations" {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusNotFound, "not_found", "404 page not found")
		return
	}
	if request.Method != http.MethodPost {
		abortMessageBody(w, controller)
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	if request.ContentLength > registrationBodyLimit {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "registration body exceeds 64 KiB")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, registrationBodyLimit))
	if err != nil {
		abortMessageBody(w, controller)
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
	if errors.Is(err, errRegistrationLimit) {
		writeAPIError(w, http.StatusServiceUnavailable, "registration_limit", "live registration limit reached")
		return
	}
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
