// Package steps is the single registration point for implemented business
// bindings.
//
// T02 implements the edge, identity and security scenarios only. Every binding
// below drives real code: a real RSA signed credential through the production
// verifier, the versioned KrakenD configuration through the edge policy
// executor, and the private gRPC surface over real mutual TLS. Scenarios that
// are not listed in features/implemented_scenarios.txt are approved
// specifications, not passing tests.
package steps

import (
	"context"

	"github.com/cucumber/godog"
)

// Initialize registers only real bindings for tags listed in the manifest.
// Godog calls it once per scenario, so each scenario gets an isolated world and
// execution order cannot matter.
func Initialize(scenario *godog.ScenarioContext) {
	w := newWorld()

	scenario.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return ctx, w.prepare()
	})
	scenario.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w.release()
		return ctx, err
	})

	registerTenantIsolation(scenario, w)
	registerPublicEdge(scenario, w)
	registerPrivateSurface(scenario, w)
}

// registerTenantIsolation covers @SCN-RNF06-001, @SCN-RNF06-002 and
// @SCN-RNF06-003.
func registerTenantIsolation(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o token é válido e contém o comerciante e o escopo exigido$`, w.givenValidMerchantCredential)
	scenario.Step(`^uma operação autorizada for solicitada$`, w.whenAuthorizedOperationIsRequested)
	scenario.Step(`^o comerciante deve ser derivado da identidade autenticada$`, w.thenMerchantIsDerivedFromTheIdentity)
	scenario.Step(`^a operação deve permanecer limitada a esse comerciante$`, w.thenOperationStaysScopedToThatMerchant)

	scenario.Step(`^que o token está "([^"]*)"$`, w.givenCredentialCondition)
	scenario.Step(`^uma operação protegida for solicitada$`, w.whenProtectedOperationIsRequested)
	scenario.Step(`^a solicitação deve ser rejeitada$`, w.thenRequestIsRejected)
	scenario.Step(`^nenhum dado financeiro deve ser alterado ou revelado$`, w.thenNoFinancialDataChangedOrLeaked)

	scenario.Step(`^que o recurso pertence a outro comerciante$`, w.givenResourceBelongsToAnotherMerchant)
	scenario.Step(`^a identidade autenticada tentar acessá-lo$`, w.whenAuthenticatedIdentityTriesToReachIt)
	scenario.Step(`^a operação deve ser negada$`, w.thenOperationIsDenied)
	scenario.Step(`^a resposta não deve revelar se o recurso existe$`, w.thenResponseHidesExistence)
	scenario.Step(`^código e corpo devem seguir o mesmo contrato de um identificador inexistente$`, w.thenStatusAndBodyMatchAnAbsentIdentifier)
	scenario.Step(`^a análise estatística de tempo não deve permitir enumeração prática de recursos$`, w.thenTimingDoesNotEnableEnumeration)
}

// registerPublicEdge covers @SCN-RNF08-002 through @SCN-RNF08-005 and
// @SCN-RNF08-008.
func registerPublicEdge(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o JWT está "([^"]*)"$`, w.givenEdgeCredentialCondition)
	scenario.Step(`^uma rota pública protegida for chamada pelo KrakenD$`, w.whenProtectedPublicRouteIsCalled)
	scenario.Step(`^a borda deve rejeitar a solicitação sem encaminhá-la ao serviço$`, w.thenEdgeRejectsWithoutForwarding)

	scenario.Step(`^que uma chamada alcançou diretamente a rede privada da Ledger API$`, w.givenCallReachedThePrivateNetworkDirectly)
	scenario.Step(`^que seu JWT não é válido para a operação$`, w.givenItsCredentialIsInvalidForTheOperation)
	scenario.Step(`^o serviço validar identidade, escopo e comerciante$`, w.whenTheServiceValidatesIdentityScopeAndMerchant)
	scenario.Step(`^a operação deve ser rejeitada$`, w.thenTheOperationIsRefused)
	scenario.Step(`^nenhum lançamento deve ser confirmado$`, w.thenNoEntryWasConfirmed)

	scenario.Step(`^que uma requisição pública válida contém "Authorization", "Idempotency-Key", "traceparent" e "tracestate"$`, w.givenValidPublicRequestCarriesTheFourHeaders)
	scenario.Step(`^o KrakenD encaminhá-la ao serviço responsável$`, w.whenTheEdgeForwardsItToTheResponsibleService)
	scenario.Step(`^o serviço deve receber os quatro cabeçalhos sem alteração semântica$`, w.thenTheServiceReceivesTheFourHeadersUnchanged)

	scenario.Step(`^que a Ledger API confirmou o lançamento mas interrompeu a resposta$`, w.givenBackendConfirmedThenBrokeTheResponse)
	scenario.Step(`^o KrakenD observar a falha do backend$`, w.whenTheEdgeObservesTheBackendFailure)
	scenario.Step(`^o gateway não deve realizar uma segunda invocação do comando POST$`, w.thenTheEdgeDoesNotInvokeTheCommandASecondTime)
	scenario.Step(`^uma repetição feita pelo cliente deve depender da mesma "Idempotency-Key"$`, w.thenAClientRepetitionDependsOnTheSameIdempotencyKey)

	scenario.Step(`^que o Keycloak está acessível internamente pela rede do Swarm$`, w.givenKeycloakIsReachableInsideTheOverlay)
	scenario.Step(`^um cliente externo consultar administração, health ou métricas pela borda pública$`, w.whenAnExternalClientAsksForAdminHealthOrMetrics)
	scenario.Step(`^o KrakenD deve rejeitar ou não possuir rota para esses caminhos$`, w.thenTheEdgeHasNoRouteForThosePaths)
	scenario.Step(`^somente os caminhos públicos necessários ao protocolo OIDC devem estar expostos$`, w.thenOnlyTheRequiredOIDCPathsAreExposed)
}

// registerPrivateSurface covers @SCN-RNF08-009 and @SCN-RNF09-004.
func registerPrivateSurface(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o RPC de watermark da Ledger pertence somente à rede interna gRPC$`, w.givenWatermarkRPCBelongsToThePrivateNetwork)
	scenario.Step(`^uma chamada sem identidade de serviço válida tentar consultá-lo$`, w.whenACallWithoutServiceIdentityTriesIt)
	scenario.Step(`^a Ledger API deve rejeitar a chamada$`, w.thenTheLedgerRefusesTheCall)
	scenario.Step(`^o KrakenD não deve possuir rota pública para esse RPC$`, w.thenTheEdgeHasNoPublicRouteForThatRPC)

	scenario.Step(`^que uma chamada interna possui trace context e deadline$`, w.givenInternalCallHasTraceContextAndDeadline)
	scenario.Step(`^atravessar um cliente e servidor gRPC$`, w.whenItCrossesAGRPCClientAndServer)
	scenario.Step(`^"traceparent" deve permanecer correlacionável em metadata$`, w.thenTraceParentStaysCorrelatableInMetadata)
	scenario.Step(`^cancelamento, deadline e limite de tamanho devem ser respeitados$`, w.thenCancellationDeadlineAndSizeLimitsAreHonoured)
	scenario.Step(`^JWT, valores e descrições não devem aparecer em traces ou logs$`, w.thenNoCredentialValueOrDescriptionAppearsInTelemetry)
}
