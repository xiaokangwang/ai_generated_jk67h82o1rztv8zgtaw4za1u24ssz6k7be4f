package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeDTLSHandshaker struct {
	err              error
	called           bool
	deadlineSet      bool
	deadlineDuration time.Duration
}

func (f *fakeDTLSHandshaker) HandshakeContext(ctx context.Context) error {
	f.called = true
	if deadline, ok := ctx.Deadline(); ok {
		f.deadlineSet = true
		f.deadlineDuration = time.Until(deadline)
	}
	return f.err
}

func TestCompleteDTLSHandshakeAddsTimeout(t *testing.T) {
	t.Parallel()

	handshaker := &fakeDTLSHandshaker{}
	if err := completeDTLSHandshake(context.Background(), handshaker); err != nil {
		t.Fatal(err)
	}
	if !handshaker.called {
		t.Fatal("expected handshake to be called")
	}
	if !handshaker.deadlineSet {
		t.Fatal("expected handshake deadline to be set")
	}
	if handshaker.deadlineDuration <= 0 || handshaker.deadlineDuration > defaultDTLSHandshakeTimeout {
		t.Fatalf("unexpected handshake deadline duration: %v", handshaker.deadlineDuration)
	}
}

func TestCompleteDTLSHandshakeKeepsShorterDeadline(t *testing.T) {
	t.Parallel()

	parentCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	handshaker := &fakeDTLSHandshaker{}
	if err := completeDTLSHandshake(parentCtx, handshaker); err != nil {
		t.Fatal(err)
	}
	if !handshaker.deadlineSet {
		t.Fatal("expected handshake deadline to be set")
	}
	if handshaker.deadlineDuration > 5*time.Second {
		t.Fatalf("expected shorter parent deadline to be preserved, got %v", handshaker.deadlineDuration)
	}
}

func TestCompleteDTLSHandshakeReturnsError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("handshake failed")
	handshaker := &fakeDTLSHandshaker{err: wantErr}
	if err := completeDTLSHandshake(context.Background(), handshaker); !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}
