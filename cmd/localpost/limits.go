package main

import "errors"

const (
	defaultRegistrationLimit = 64
	defaultUploadLimit       = 16
)

var errRegistrationLimit = errors.New("live registration limit reached")

// uploadSlots bounds uploads across all mailboxes, without queuing senders.
// A slot remains held through publication and the temporary-file cleanup attempt.
// At 10 MiB per upload, 16 slots bound active payloads to 160 MiB; this does not
// bound retained deliveries, failed-cleanup leftovers, or total process memory.
type uploadSlots struct {
	held chan struct{}
}

func newUploadSlots(limit int) *uploadSlots {
	return &uploadSlots{held: make(chan struct{}, limit)}
}

func (s *uploadSlots) acquire() bool {
	select {
	case s.held <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *uploadSlots) release() {
	<-s.held
}
