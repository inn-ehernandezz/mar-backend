// Package inference routes model requests to a preferred accelerator with a CPU fallback.
package inference

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	gpuAttemptTimeout = 12 * time.Second
	gpuRecoveryDelay  = 20 * time.Second
)

// GPU prefers its server while it has a free slot. The caller's request targets the
// CPU server and remains replayable until a GPU attempt has succeeded.
type GPU struct {
	url     string
	slots   chan struct{}
	retryAt atomic.Int64
}

func NewGPU(baseURL string, concurrent int) *GPU {
	if baseURL == "" {
		return nil
	}
	return &GPU{url: strings.TrimRight(baseURL, "/"), slots: make(chan struct{}, concurrent)}
}

func (g *GPU) Do(client *http.Client, request *http.Request, path string) (*http.Response, error) {
	if g == nil || request.GetBody == nil || time.Now().UnixNano() < g.retryAt.Load() {
		return client.Do(request)
	}
	select {
	case g.slots <- struct{}{}:
	default:
		return client.Do(request)
	}

	ctx, cancel := context.WithTimeout(request.Context(), gpuAttemptTimeout)
	release := func() {
		cancel()
		<-g.slots
	}
	body, err := request.GetBody()
	if err != nil {
		release()
		return client.Do(request)
	}
	gpuRequest, err := http.NewRequestWithContext(ctx, request.Method, g.url+path, body)
	if err != nil {
		_ = body.Close()
		release()
		return client.Do(request)
	}
	gpuRequest.Header = request.Header.Clone()
	gpuRequest.ContentLength = request.ContentLength
	response, err := client.Do(gpuRequest)
	if err == nil && response.StatusCode < http.StatusInternalServerError &&
		response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusRequestTimeout {
		_ = request.Body.Close()
		response.Body = &gpuResponseBody{ReadCloser: response.Body, release: release}
		return response, nil
	}
	if response != nil {
		_ = response.Body.Close()
	}
	release()
	if err := request.Context().Err(); err != nil {
		_ = request.Body.Close()
		return nil, err
	}
	g.retryAt.Store(time.Now().Add(gpuRecoveryDelay).UnixNano())
	slog.WarnContext(request.Context(), "inferencia GPU no disponible, usando CPU", "path", path)
	return client.Do(request)
}

type gpuResponseBody struct {
	io.ReadCloser
	release func()
}

func (b *gpuResponseBody) Close() error {
	err := b.ReadCloser.Close()
	if b.release != nil {
		b.release()
		b.release = nil
	}
	return err
}
