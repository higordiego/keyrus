package steps

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
	"github.com/higordiegoti/keyrus/test/support/edgesim"
	"github.com/higordiegoti/keyrus/test/support/internalgrpc"
	"github.com/higordiegoti/keyrus/test/support/protectedapi"
	"github.com/higordiegoti/keyrus/test/support/runtimeevidence"
)

// Fixed identity fixture shared by the security scenarios.
const (
	issuerURL        = "https://edge.cashflow.local/realms/cashflow"
	publicAudience   = "cashflow-public-api"
	internalAudience = "cashflow-internal-api"

	merchantA = "11111111-1111-4111-8111-111111111111"
	merchantB = "22222222-2222-4222-8222-222222222222"

	resourceOfA = "entry-owned-by-a"
	resourceOfB = "entry-owned-by-b"
	absentID    = "entry-that-never-existed"

	krakendConfigPath = "../../deploy/edge/krakend/krakend.json"
)

// The issuer holds RSA key material and is immutable once built, so one instance
// is shared by every scenario instead of paying for key generation each time.
var (
	sharedIssuerOnce sync.Once
	sharedIssuer     *authtest.Issuer
	sharedIssuerErr  error
)

func issuer() (*authtest.Issuer, error) {
	sharedIssuerOnce.Do(func() {
		sharedIssuer, sharedIssuerErr = authtest.NewIssuer(issuerURL)
	})
	return sharedIssuer, sharedIssuerErr
}

// world is the per-scenario state. Godog builds a fresh one for every scenario,
// so nothing leaks between them and execution order does not matter.
type world struct {
	evidence    runtimeevidence.Evidence
	scenarioTag string
	caseID      string
	issuer      *authtest.Issuer
	service     *protectedapi.Service

	// credential under test
	token          string
	tokenCondition string

	// direct-to-service exchange
	response       *http.Response
	responseBody   []byte
	baselineStatus int
	baselineBody   []byte
	timingVerdict  string

	// edge exchange
	gateway       *edgesim.Gateway
	edgeResponse  *http.Response
	edgeRoute     string
	blockedRoutes map[string]int

	// private gRPC exchange
	harness      *internalgrpc.Harness
	internalErr  error
	internalNote string
	logOutput    string
}

func newWorld() *world {
	return &world{
		service: protectedapi.New(map[string]string{
			resourceOfA: merchantA,
			resourceOfB: merchantB,
		}),
		blockedRoutes: make(map[string]int),
	}
}

func (w *world) prepare(scenario *godog.Scenario) error {
	evidence, err := runtimeevidence.LoadForSource(os.Getenv("CASHFLOW_RUNTIME_EVIDENCE_FILE"), filepath.Join("..", ".."))
	if err != nil {
		return fmt.Errorf("load real runtime evidence: %w", err)
	}
	for _, tag := range scenario.Tags {
		if _, present := evidence.Scenarios[tag.Name]; present {
			w.scenarioTag = tag.Name
			break
		}
	}
	if w.scenarioTag == "" {
		return fmt.Errorf("scenario %q has no real runtime evidence", scenario.Name)
	}
	w.evidence = evidence
	w.caseID = runtimeevidence.DefaultCase
	return nil
}

func (w *world) require(oracle string) error {
	return runtimeevidence.Require(w.evidence, w.scenarioTag, w.caseID, oracle)
}

func (w *world) requireCondition(condition string) error {
	w.caseID = condition
	return w.require("condition_exercised")
}

func (w *world) release() {
	if w.harness != nil {
		w.harness.Stop()
		w.harness = nil
	}
}

// publicVerifier is the policy every public HTTP adapter applies.
func (w *world) publicVerifier() (*auth.Verifier, error) {
	return auth.NewVerifier(auth.VerifierConfig{
		Issuer:   issuerURL,
		Audience: publicAudience,
		Keys:     w.issuer.Keys(),
		Merchant: auth.MerchantRequired,
	})
}

// internalVerifier is the policy the private gRPC surface applies. It forbids
// the merchant claim so an end-user token cannot be replayed there.
func (w *world) internalVerifier() (*auth.Verifier, error) {
	return auth.NewVerifier(auth.VerifierConfig{
		Issuer:   issuerURL,
		Audience: internalAudience,
		Keys:     w.issuer.Keys(),
		Merchant: auth.MerchantForbidden,
	})
}

func (w *world) edge() (*edgesim.Gateway, error) {
	if w.gateway != nil {
		return w.gateway, nil
	}
	gateway, err := edgesim.New(krakendConfigPath, w.issuer.Keys())
	if err != nil {
		return nil, fmt.Errorf("load edge configuration: %w", err)
	}
	w.gateway = gateway
	return gateway, nil
}
