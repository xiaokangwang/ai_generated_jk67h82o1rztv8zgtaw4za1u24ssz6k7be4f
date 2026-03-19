package main

import (
	"context"
	"time"
)

const defaultDTLSHandshakeTimeout = 30 * time.Second

type dtlsHandshaker interface {
	HandshakeContext(context.Context) error
}

func completeDTLSHandshake(ctx context.Context, conn dtlsHandshaker) error {
	handshakeCtx := ctx
	cancel := func() {}

	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultDTLSHandshakeTimeout {
		handshakeCtx, cancel = context.WithTimeout(ctx, defaultDTLSHandshakeTimeout)
	}
	defer cancel()

	return conn.HandshakeContext(handshakeCtx)
}
