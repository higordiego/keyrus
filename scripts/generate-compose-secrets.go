package main

import (
	"fmt"
	"os"

	"github.com/higordiegoti/keyrus/test/support/testpki"
)

func main() {
	err := os.MkdirAll("secrets/certs", 0755)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll("deploy/keycloak", 0755)
	if err != nil {
		panic(err)
	}

	_, err = testpki.New("secrets/certs")
	if err != nil {
		panic(err)
	}

	// Create a dummy consolidation secret
	err = os.WriteFile("secrets/consolidation-client-secret", []byte("consolidation-secret-123"), 0644)
	if err != nil {
		panic(err)
	}

	fmt.Println("Secrets and PKI generated successfully in ./secrets")
}
