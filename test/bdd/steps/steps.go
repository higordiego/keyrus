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

	scenario.Before(func(ctx context.Context, current *godog.Scenario) (context.Context, error) {
		return ctx, w.prepare(current)
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
	scenario.Step(`^que o token é válido e contém o comerciante e o escopo exigido$`, w.runtimeStep)
	scenario.Step(`^uma operação autorizada for solicitada$`, w.runtimeStep)
	scenario.Step(`^o comerciante deve ser derivado da identidade autenticada$`, w.runtimeStep)
	scenario.Step(`^a operação deve permanecer limitada a esse comerciante$`, w.runtimeStep)

	scenario.Step(`^que o token está "([^"]*)"$`, w.runtimeStepWithCondition)
	scenario.Step(`^uma operação protegida for solicitada$`, w.runtimeStep)
	scenario.Step(`^a solicitação deve ser rejeitada$`, w.runtimeStep)
	scenario.Step(`^nenhum dado financeiro deve ser alterado ou revelado$`, w.runtimeStep)

	scenario.Step(`^que o recurso pertence a outro comerciante$`, w.runtimeStep)
	scenario.Step(`^a identidade autenticada tentar acessá-lo$`, w.runtimeStep)
	scenario.Step(`^a operação deve ser negada$`, w.runtimeStep)
	scenario.Step(`^a resposta não deve revelar se o recurso existe$`, w.runtimeStep)
	scenario.Step(`^código e corpo devem seguir o mesmo contrato de um identificador inexistente$`, w.runtimeStep)
	scenario.Step(`^a análise estatística de tempo não deve permitir enumeração prática de recursos$`, w.runtimeStep)
}

// registerPublicEdge covers @SCN-RNF08-002 through @SCN-RNF08-005 and
// @SCN-RNF08-008.
func registerPublicEdge(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o JWT está "([^"]*)"$`, w.runtimeStepWithCondition)
	scenario.Step(`^uma rota pública protegida for chamada pelo KrakenD$`, w.runtimeStep)
	scenario.Step(`^a borda deve rejeitar a solicitação sem encaminhá-la ao serviço$`, w.runtimeStep)

	scenario.Step(`^que uma chamada alcançou diretamente a rede privada da Ledger API$`, w.runtimeStep)
	scenario.Step(`^que seu JWT não é válido para a operação$`, w.runtimeStep)
	scenario.Step(`^o serviço validar identidade, escopo e comerciante$`, w.runtimeStep)
	scenario.Step(`^a operação deve ser rejeitada$`, w.runtimeStep)
	scenario.Step(`^nenhum lançamento deve ser confirmado$`, w.runtimeStep)

	scenario.Step(`^que uma requisição pública válida contém "Authorization", "Idempotency-Key", "traceparent" e "tracestate"$`, w.runtimeStep)
	scenario.Step(`^o KrakenD encaminhá-la ao serviço responsável$`, w.runtimeStep)
	scenario.Step(`^o serviço deve receber os quatro cabeçalhos sem alteração semântica$`, w.runtimeStep)

	scenario.Step(`^que a Ledger API confirmou o lançamento mas interrompeu a resposta$`, w.runtimeStep)
	scenario.Step(`^o KrakenD observar a falha do backend$`, w.runtimeStep)
	scenario.Step(`^o gateway não deve realizar uma segunda invocação do comando POST$`, w.runtimeStep)
	scenario.Step(`^uma repetição feita pelo cliente deve depender da mesma "Idempotency-Key"$`, w.runtimeStep)

	scenario.Step(`^que o Keycloak está acessível internamente pela rede do Swarm$`, w.runtimeStep)
	scenario.Step(`^um cliente externo consultar administração, health ou métricas pela borda pública$`, w.runtimeStep)
	scenario.Step(`^o KrakenD deve rejeitar ou não possuir rota para esses caminhos$`, w.runtimeStep)
	scenario.Step(`^somente os caminhos públicos necessários ao protocolo OIDC devem estar expostos$`, w.runtimeStep)
}

// registerPrivateSurface covers @SCN-RNF08-009 and @SCN-RNF09-004.
func registerPrivateSurface(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o RPC de watermark da Ledger pertence somente à rede interna gRPC$`, w.runtimeStep)
	scenario.Step(`^uma chamada sem identidade de serviço válida tentar consultá-lo$`, w.runtimeStep)
	scenario.Step(`^a Ledger API deve rejeitar a chamada$`, w.runtimeStep)
	scenario.Step(`^o KrakenD não deve possuir rota pública para esse RPC$`, w.runtimeStep)

	scenario.Step(`^que uma chamada interna possui trace context e deadline$`, w.runtimeStep)
	scenario.Step(`^atravessar um cliente e servidor gRPC$`, w.runtimeStep)
	scenario.Step(`^"traceparent" deve permanecer correlacionável em metadata$`, w.runtimeStep)
	scenario.Step(`^cancelamento, deadline e limite de tamanho devem ser respeitados$`, w.runtimeStep)
	scenario.Step(`^JWT, valores e descrições não devem aparecer em traces ou logs$`, w.runtimeStep)
}
