package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeGracefulShutdownCompletesInFlightRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	started := make(chan struct{})
	release := make(chan struct{})
	server := New(Options{})
	server.router = chi.NewRouter()
	server.router.Get("/in-flight", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "completed")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	response := make(chan struct {
		status int
		body   string
		err    error
	}, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String() + "/in-flight")
		if err != nil {
			response <- struct {
				status int
				body   string
				err    error
			}{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		response <- struct {
			status int
			body   string
			err    error
		}{status: resp.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the handler")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server stopped before the in-flight request completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)

	select {
	case result := <-response:
		require.NoError(t, result.err)
		assert.Equal(t, http.StatusOK, result.status)
		assert.Equal(t, "completed", result.body)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete")
	}
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after the request completed")
	}
}

func TestListenAndServeReturnsAddressInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	server := New(Options{Addr: listener.Addr().String()})
	assert.Error(t, server.ListenAndServe(context.Background()))
}

func TestListenAndServeCancelledContextReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := New(Options{Addr: "127.0.0.1:0"})
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not honor the canceled context")
	}
}
