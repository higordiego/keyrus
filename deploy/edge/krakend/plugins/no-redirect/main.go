// Package main exposes the KrakenD HTTP-client plugin used by the browser
// login action. Keycloak must return its redirect to the browser; following it
// inside the gateway would send an application callback to the backend client.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

const pluginName = "cashflow-no-redirect"

// ClientRegisterer is discovered by KrakenD's Go plugin loader.
var ClientRegisterer = registerer(pluginName)

type registerer string

func (r registerer) RegisterClients(register func(
	name string,
	handler func(context.Context, map[string]interface{}) (http.Handler, error),
)) {
	register(string(r), r.newClient)
}

func (r registerer) newClient(_ context.Context, config map[string]interface{}) (http.Handler, error) {
	name, ok := config["name"].(string)
	if !ok || name != string(r) {
		return nil, fmt.Errorf("unexpected HTTP client plugin %q", name)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		outbound := request.Clone(request.Context())
		outbound.RequestURI = ""
		response, err := client.Do(outbound)
		if err != nil {
			http.Error(writer, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()

		for name, values := range response.Header {
			for _, value := range values {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.StatusCode)
		_, _ = io.Copy(writer, response.Body)
	}), nil
}
