package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"uuid"
)

const messageBodyLimit int64 = 10 << 20
const messageIdleTimeout = 30 * time.Second

var errMessageStorage = errors.New("message storage failure")

type messageFile interface {
	io.WriteCloser
	Name() string
}

type messageStorage struct {
	createTemp func(string, string) (messageFile, error)
	link       func(string, string) error
	remove     func(string) error
}

type messageHandler struct {
	directory   string
	idleTimeout time.Duration
	mailbox     *registration
	uploads     *uploadSlots
	filename    func() string
	storage     messageStorage
}

type messageReceipt struct {
	Filename string `json:"filename"`
	Bytes    int64  `json:"bytes"`
}

func newMessageHandler(directory string) *messageHandler {
	return &messageHandler{
		directory:   directory,
		idleTimeout: messageIdleTimeout,
		filename:    func() string { return "localpost_" + uuid.NewV7().String() + ".dat" },
		storage: messageStorage{
			createTemp: func(directory, pattern string) (messageFile, error) {
				return os.CreateTemp(directory, pattern)
			},
			link:   os.Link,
			remove: os.Remove,
		},
	}
}

func (h *messageHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	controller := http.NewResponseController(w)
	if h.mailbox != nil {
		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		request = request.WithContext(ctx)
		active := &activeRequest{abort: func() {
			cancel()
			// Do not close the shared listener or another registration's connections.
			_ = controller.SetReadDeadline(time.Now())
		}}
		if !h.mailbox.admitRequest(active) {
			abortMessageBody(w, controller)
			writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mailbox is stopping")
			return
		}
		defer h.mailbox.finishedRequest(active)
	}
	path := "/messages"
	if h.mailbox != nil {
		path = "/registrations/" + h.mailbox.ID + "/messages"
	}
	if request.URL.Path != path {
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
	if err != nil || (mediaType != "application/json" && mediaType != "application/octet-stream") {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json or application/octet-stream")
		return
	}
	if request.ContentLength > messageBodyLimit {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "message body exceeds 10 MiB")
		return
	}
	if h.uploads != nil {
		if !h.uploads.acquire() {
			abortMessageBody(w, controller)
			writeAPIError(w, http.StatusServiceUnavailable, "upload_limit", "active upload limit reached")
			return
		}
		defer h.uploads.release()
	}
	if err := h.setReadDeadline(controller, time.Now().Add(h.idleTimeout)); err != nil {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusInternalServerError, "delivery_failed", "unable to enforce message read timeout")
		return
	}

	file, err := h.storage.createTemp(h.directory, ".localpost-upload-*.dat")
	if err != nil {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusInternalServerError, "delivery_failed", "unable to create message file")
		return
	}
	// Cleanup is best effort. A published message remains successful even if the
	// temporary hard link cannot be removed; it must not invite a duplicate retry.
	defer func() { _ = h.storage.remove(file.Name()) }()
	size, readErr := copyMessageWithDeadline(file, http.MaxBytesReader(w, request.Body, messageBodyLimit), func(deadline time.Time) error {
		return h.setReadDeadline(controller, deadline)
	}, h.idleTimeout)
	closeErr := file.Close()
	if readErr != nil {
		abortMessageBody(w, controller)
		var tooLarge *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.Is(readErr, errMessageStorage):
			writeAPIError(w, http.StatusInternalServerError, "delivery_failed", "unable to write message file")
		case errors.As(readErr, &tooLarge):
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "message body exceeds 10 MiB")
		case errors.As(readErr, &timeout) && timeout.Timeout():
			writeAPIError(w, http.StatusRequestTimeout, "request_timeout", "message body read timed out")
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "unable to receive complete message body")
		}
		return
	}
	// Only a fully consumed body is safe for keep-alive reuse. Rejected bodies
	// retain an expired deadline so net/http's final body drain cannot stall.
	_ = h.setReadDeadline(controller, time.Time{})
	if closeErr != nil || request.Context().Err() != nil {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusInternalServerError, "delivery_failed", "unable to complete message file")
		return
	}
	filename := h.filename()
	// Linking a closed, complete file publishes atomically and never replaces an
	// existing destination. Unsupported filesystems fail rather than weakening it.
	if err := h.publish(file.Name(), filename); err != nil {
		abortMessageBody(w, controller)
		writeAPIError(w, http.StatusInternalServerError, "delivery_failed", "unable to publish message file")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Losing the response cannot undo a successfully published message.
	_ = json.NewEncoder(w).Encode(messageReceipt{Filename: filename, Bytes: size})
}

// Serialize deadline refreshes with force so upload progress cannot undo the
// forced cutoff. The controller overrides the server's total read timeout for
// upload routes, preserving the per-progress idle deadline.
func (h *messageHandler) setReadDeadline(controller *http.ResponseController, deadline time.Time) error {
	if h.mailbox != nil {
		h.mailbox.mu.Lock()
		defer h.mailbox.mu.Unlock()
		if h.mailbox.forced {
			return context.Canceled
		}
	}
	return controller.SetReadDeadline(deadline)
}

func (h *messageHandler) publish(temporary, filename string) error {
	if h.mailbox != nil {
		h.mailbox.mu.Lock()
		defer h.mailbox.mu.Unlock()
		if h.mailbox.forced {
			return errors.New("mailbox was forcibly stopped")
		}
	}
	return h.storage.link(temporary, filepath.Join(h.directory, filename))
}

// abortMessageBody bounds net/http's implicit request-body cleanup as well as
// our reads; Connection: close by itself does not prevent that final drain.
func abortMessageBody(w http.ResponseWriter, controller *http.ResponseController) {
	w.Header().Set("Connection", "close")
	// Real mailbox servers support read deadlines; a failed controller is already
	// an internal error and has no safe keep-alive continuation.
	_ = controller.SetReadDeadline(time.Now())
}

func copyMessage(destination io.Writer, source io.Reader, controller *http.ResponseController, idleTimeout time.Duration) (int64, error) {
	return copyMessageWithDeadline(destination, source, controller.SetReadDeadline, idleTimeout)
}

func copyMessageWithDeadline(destination io.Writer, source io.Reader, setDeadline func(time.Time) error, idleTimeout time.Duration) (int64, error) {
	var total int64
	buffer := make([]byte, 32<<10)
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, err := destination.Write(buffer[:count])
			total += int64(written)
			if err != nil {
				return total, errors.Join(errMessageStorage, err)
			}
			if written != count {
				return total, errors.Join(errMessageStorage, io.ErrShortWrite)
			}
			// Progress extends the idle deadline, never an empty read.
			if err := setDeadline(time.Now().Add(idleTimeout)); err != nil {
				return total, errors.Join(errMessageStorage, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
