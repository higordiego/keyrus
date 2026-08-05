package main

import (
	"fmt"
	"os"
	"time"

	"github.com/higordiegoti/keyrus/test/support/testpki"
)

// localComposeCertValidity is long enough to survive a full working
// session's `docker compose up`. testpki.New's default 2-hour window is
// meant for a single automated test run and silently expired mid-session
// during local manual testing (found while gathering T11 load evidence:
// every authenticated request started failing TLS verification once the CA
// aged past its NotAfter). Re-run this script if a Compose stack has been up
// long enough to outlive even this window.
const localComposeCertValidity = 30 * 24 * time.Hour

func main() {
	err := os.MkdirAll("secrets/certs", 0755)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll("deploy/keycloak", 0755)
	if err != nil {
		panic(err)
	}

	_, err = testpki.NewWithValidity("secrets/certs", localComposeCertValidity)
	if err != nil {
		panic(err)
	}

	err = os.WriteFile("secrets/consolidation-client-secret", []byte("consolidation-secret-123"), 0644)
	if err != nil {
		panic(err)
	}
	err = os.WriteFile("secrets/reconciliation-client-secret", []byte("reconciliation-secret-123"), 0644)
	if err != nil {
		panic(err)
	}

	fmt.Println("Secrets and PKI generated successfully in ./secrets")
}
