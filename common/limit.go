package common

import (
	"context"
	"golang.org/x/time/rate"
	"io"
)

type RateLimiter struct {
	rw      io.ReadWriter
	limiter *rate.Limiter
}

func NewRateLimited(rw io.ReadWriter, limiter *rate.Limiter) *RateLimiter {
	return &RateLimiter{
		rw:      rw,
		limiter: limiter,
	}
}

func (r *RateLimiter) Read(p []byte) (int, error) {
	if r.rw == nil {
		return 0, io.ErrUnexpectedEOF
	}

	n, err := r.rw.Read(p)
	if n > 0 && r.limiter != nil {
		err := r.limiter.WaitN(context.Background(), n)
		if err != nil {
			return n, err
		}
	}
	return n, err
}

func (r *RateLimiter) Write(p []byte) (int, error) {
	if r.rw == nil {
		return 0, io.ErrUnexpectedEOF
	}

	if r.limiter != nil {
		err := r.limiter.WaitN(context.Background(), len(p))
		if err != nil {
			return 0, err
		}
	}
	return r.rw.Write(p)
}
