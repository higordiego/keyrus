// Command faultbackend is an E2E-only HTTP fixture. It records the exact edge
// request and can close a connection after a durable in-memory commit so the
// real KrakenD retry behavior is observable.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

const (
	headerKey = "e2e-header-key"
	eofKey    = "e2e-eof-key"
)

type requestHeaders struct {
	Authorization  string `json:"authorization"`
	IdempotencyKey string `json:"idempotency_key"`
	TraceParent    string `json:"traceparent"`
	TraceState     string `json:"tracestate"`
}

type keyState struct {
	Invocations int `json:"invocations"`
	Commits     int `json:"commits"`
	Replays     int `json:"replays"`
}

type state struct {
	mu      sync.Mutex
	Headers requestHeaders      `json:"headers"`
	Keys    map[string]keyState `json:"keys"`
}

func main() {
	fixture := &state{Keys: make(map[string]keyState)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__fixture/ready", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /__fixture/state", fixture.writeState)
	mux.HandleFunc("POST /v1/entries", fixture.postEntry)
	server := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 2_000_000_000}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic("fault backend stopped")
	}
}

func (s *state) postEntry(writer http.ResponseWriter, request *http.Request) {
	_, _ = io.Copy(io.Discard, request.Body)
	_ = request.Body.Close()
	key := request.Header.Get("Idempotency-Key")

	s.mu.Lock()
	current := s.Keys[key]
	current.Invocations++
	s.Keys[key] = current
	if key == headerKey {
		s.Headers = requestHeaders{
			Authorization:  request.Header.Get("Authorization"),
			IdempotencyKey: key,
			TraceParent:    request.Header.Get("traceparent"),
			TraceState:     request.Header.Get("tracestate"),
		}
		s.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if key != eofKey {
		s.mu.Unlock()
		http.Error(writer, "unknown test case", http.StatusBadRequest)
		return
	}
	if current.Commits == 0 {
		current.Commits = 1
		s.Keys[key] = current
		s.mu.Unlock()
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			panic("HTTP server does not support hijacking")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			panic("failed to interrupt committed response")
		}
		_ = connection.Close()
		return
	}
	current.Replays++
	s.Keys[key] = current
	s.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]bool{"idempotent_replay": true})
}

func (s *state) writeState(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	copy := struct {
		Headers requestHeaders      `json:"headers"`
		Keys    map[string]keyState `json:"keys"`
	}{Headers: s.Headers, Keys: make(map[string]keyState, len(s.Keys))}
	for key, value := range s.Keys {
		copy.Keys[key] = value
	}
	s.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(copy)
}
