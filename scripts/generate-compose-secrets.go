package main

import (
	"encoding/json"
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

	// We also need a basic realm json. 
	// For simplicity, we just copy the one from testdata or create a minimal one.
	// Since we don't have the testdata template easily accessible here without knowing its path,
	// let's just write a minimal valid realm.
	realm := map[string]interface{}{
		"id": "cashflow",
		"realm": "cashflow",
		"enabled": true,
		"clients": []map[string]interface{}{
			{
				"clientId": "cashflow-consolidation-svc",
				"enabled": true,
				"clientAuthenticatorType": "client-secret",
				"secret": "consolidation-secret-123",
				"serviceAccountsEnabled": true,
			},
		},
	}
	
	b, _ := json.MarshalIndent(realm, "", "  ")
	err = os.WriteFile("deploy/keycloak/realm-cashflow.json", b, 0644)
	if err != nil {
		panic(err)
	}

	fmt.Println("Secrets and PKI generated successfully in ./secrets and ./deploy/keycloak")
}
