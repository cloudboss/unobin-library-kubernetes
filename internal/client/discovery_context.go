package client

import (
	"context"
	"io"
	"net/http"
)

type discoveryTransport struct {
	context context.Context
	base    http.RoundTripper
}

func (t discoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.context.Err(); err != nil {
		return nil, err
	}
	// Discovery's mapper interface has no context argument. Keep its request
	// cancellation active alongside the operation context until the body closes.
	ctx, cancel := context.WithCancel(t.context)
	stop := context.AfterFunc(request.Context(), cancel)
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil || response == nil || response.Body == nil {
		stop()
		cancel()
		return response, err
	}
	response.Body = &discoveryBody{ReadCloser: response.Body, stop: stop, cancel: cancel}
	return response, nil
}

type discoveryBody struct {
	io.ReadCloser
	stop   func() bool
	cancel context.CancelFunc
}

func (b *discoveryBody) Close() error {
	defer b.cancel()
	b.stop()
	return b.ReadCloser.Close()
}
