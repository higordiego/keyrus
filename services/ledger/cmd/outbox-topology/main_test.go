package main

import (
	"context"
	"strings"
	"testing"
)

func TestTopologyCommandFailsClosedBeforeConnecting(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		env       map[string]string
		want      string
	}{
		{name: "operation", arguments: []string{"drain"}, want: "usage"},
		{name: "URL", arguments: []string{"upgrade"}, want: "OUTBOX_RABBITMQ_URL is required"},
		{name: "boolean", arguments: []string{"upgrade"}, env: map[string]string{
			"OUTBOX_RABBITMQ_URL":            "amqp://user:pass@localhost/vhost",
			"OUTBOX_RABBITMQ_ALLOW_INSECURE": "sometimes",
		}, want: "must be a boolean"},
		{name: "mTLS pair", arguments: []string{"rollback"}, env: map[string]string{
			"OUTBOX_RABBITMQ_URL":       "amqps://user:pass@localhost/vhost",
			"OUTBOX_RABBITMQ_CERT_FILE": "/tmp/client.crt",
		}, want: "configured together"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(name string) string { return test.env[name] }
			if err := run(context.Background(), test.arguments, getenv); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fail-closed error mismatch: got=%v want substring=%q", err, test.want)
			}
		})
	}
}
