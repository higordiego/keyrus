package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientReturnsBackendRedirectWithoutFollowingIt(t *testing.T) {
	var callbackRequests int
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			if request.Method != http.MethodPost || request.URL.RawQuery != "session=expected" {
				t.Fatalf("unexpected backend request %s %s", request.Method, request.URL.String())
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "username=merchant" {
				t.Fatalf("unexpected backend body %q", body)
			}
			writer.Header().Add("Set-Cookie", "AUTH_SESSION_ID=deleted")
			writer.Header().Set("Location", backendURL(request)+"/callback?code=expected")
			writer.WriteHeader(http.StatusFound)
		case "/callback":
			callbackRequests++
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer backend.Close()

	handler, err := registerer(pluginName).newClient(context.Background(), map[string]interface{}{"name": pluginName})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, backend.URL+"/login?session=expected", strings.NewReader("username=merchant"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	if callbackRequests != 0 {
		t.Fatalf("backend callback requests = %d, want 0", callbackRequests)
	}
	if got := recorder.Header().Get("Location"); got != backend.URL+"/callback?code=expected" {
		t.Fatalf("Location = %q", got)
	}
	if got := recorder.Header().Get("Set-Cookie"); got != "AUTH_SESSION_ID=deleted" {
		t.Fatalf("Set-Cookie = %q", got)
	}
}

func TestClientRejectsUnexpectedRegistrationName(t *testing.T) {
	_, err := registerer(pluginName).newClient(context.Background(), map[string]interface{}{"name": "other"})
	if err == nil {
		t.Fatal("expected registration error")
	}
}

func backendURL(request *http.Request) string {
	return "http://" + request.Host
}
