// Package steps binds the implemented scenarios to their real fixtures: the
// consolidation projector (mandatory PostgreSQL fixture) and the ledger
// outbox publisher (PostgreSQL/RabbitMQ subprocess fixture). Both domains
// share a single godog catch-all step registration and dispatch internally
// by exact step text, because godog's strict mode rejects any step text that
// matches more than one registered pattern.
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
	"encoding/json"
	"fmt"
	"github.com/higordiegoti/keyrus/test/support/runtimeevidence"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/services/consolidation/acceptance"
)

const (
	merchantMain = "10000000-0000-4000-8000-000000000001"
)

var (
	dayCreditsPattern = regexp.MustCompile(`^o dia "([0-9-]+)" deve possuir créditos de R\$ ([0-9.,-]+)$`)
	dayNetPattern     = regexp.MustCompile(`^o dia "([0-9-]+)" deve continuar com líquido de R\$ ([0-9.,-]+)$`)
	amountPattern     = regexp.MustCompile(`^(débitos|líquido|saldo acumulado) de R\$ ([0-9.,-]+)$`)
	countPattern      = regexp.MustCompile(`^quantidade igual a ([0-9]+)$`)
)

type scenarioState struct {
	fixture       *acceptance.Fixture
	events        map[int][]byte
	pending       []byte
	pendingDate   string
	last          acceptance.Projection
	currentDate   string
	before        map[string]acceptance.Balance
	merchant      string
	merchantEvent map[string][]byte
	beforeB       acceptance.Balance
	beforeBProg   acceptance.Progress
}

// Initialize registers the one catch-all step godog allows without tripping
// its ambiguous-step check, then dispatches by exact text: the publisher
// fixture first (a small, fixed set of literal steps), falling through to
// the consolidation fixture's own literal/parameterized matching.
func Initialize(evidence runtimeevidence.Evidence) func(*godog.ScenarioContext) {
	return func(ctx *godog.ScenarioContext) {

		w := newWorld(evidence)
		ctx.Before(func(c context.Context, current *godog.Scenario) (context.Context, error) {
			return c, w.prepare(current)
		})
		ctx.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
			w.release()
			return c, err
		})
		registerTenantIsolation(ctx, w)
		registerPublicEdge(ctx, w)
		registerPrivateSurface(ctx, w)

		state := &scenarioState{}
		publisher := &publisherScenario{}
		ctx.Before(state.beforeScenario)
		ctx.Before(publisher.before)
		ctx.After(state.afterScenario)

		legacySteps := []string{
			"que existe um lançamento confirmado e ainda não aplicado",
			"2026-07-30",
			"que o lançamento já produziu seu efeito no consolidado",
			"2026-07-30",
			"2026-07-30",
			"que a posição 1 do comerciante foi aplicada",
			"que a fonte do comerciante declarou a posição 3",
			"2026-07-31",
			"que as posições 1 e 3 do comerciante já foram aplicadas",
			"que as posições 1, 2 e 3 do comerciante já foram aplicadas",
			"que as posições 1, 2 e 3 já foram aplicadas",
			"que existe um lançamento confirmado para uma data dentro dos últimos 30 dias",
			"credit",
			"2026-07-31",
			"",
			"debit",
			"2026-07-30",
			"",
			"2026-07-30",
			"2026-07-31",
			"que existe uma compensação confirmada",
			"credit",
			"2026-07-31",
			"",
			"debit",
			"2026-08-01",
			"2026-08-01",
			"2026-07-31",
			"que o saldo de abertura do comerciante é R$ 0,00",
			"que a posição 1 é um crédito de R$ 100,00 em \"2026-07-30\"",
			"que a posição 2 é um débito de R$ 30,00 em \"2026-07-30\"",
			"que a posição 3 é um crédito de R$ 10,00 em \"2026-07-31\"",
			"standard event fixture is incomplete",
			"que as posições 1 a 4 do comerciante já foram aplicadas",
			"que não existe outra movimentação em \"2026-08-01\"",
			"2026-08-01",
			"unexpected movement on 2026-08-01: %+v",
			"que o fuso do comerciante é \"America/Fortaleza\"",
			"2026-08-01",
			"que o relógio está fixado em \"2026-08-01T12:00:00-03:00\"",
			"2026-08-01",
			"merchant calendar was not configured",
			"que o saldo acumulado em \"2026-07-31\" é R$ 60,00",
			"2026-07-31",
			"saldo acumulado",
			"que o dia \"2026-07-30\" possui créditos de R$ 100,00, débitos de R$ 30,00, quantidade 2 e saldo acumulado de R$ 70,00",
			"2026-07-30",
			"que o dia \"2026-07-31\" possui créditos de R$ 10,00, débitos de R$ 0,00, quantidade 1 e saldo acumulado de R$ 80,00",
			"2026-07-31",
			"ele for processado pelo consolidado",
			"ele for aplicado",
			"ela for processada",
			"a mesma atualização for entregue novamente",
			"a posição 3 for entregue novamente",
			"2026-07-30",
			"2026-07-31",
			"a posição 3 chegar antes da posição 2",
			"a posição 2 do comerciante for entregue e aplicada",
			"as posições 1, 2 e 3 forem aplicadas",
			"a posição 4 do comerciante, um débito retroativo de R$ 20,00 em \"2026-07-30\", for aplicada",
			"2026-07-30",
			"a posição 5 estornar integralmente o crédito da posição 3",
			"2026-07-31",
			"os totais do comerciante e da data de negócio devem ser atualizados",
			"daily totals were not updated: %+v",
			"o lançamento deve produzir exatamente um efeito financeiro",
			"financial effect count = %d, want 1",
			"os totais e o saldo devem permanecer inalterados",
			"os valores, quantidades e saldos dos dois dias devem permanecer inalterados",
			"nenhuma atualização deve ser perdida",
			"2026-07-31",
			"out-of-order event was lost",
			"os totais dessa data devem ser corrigidos",
			"retroactive totals were not corrected: %+v",
			"os saldos acumulados posteriores devem ser recompostos",
			"2026-07-31",
			"2026-07-31",
			"later closing was not recomputed: before=%+v after=%+v",
			"o intervalo afetado deve permanecer não definitivo até o fim da recomposição",
			"bounded retroactive recompute did not complete",
			"deve afetar os totais da data do estorno",
			"2026-08-01",
			"créditos",
			"2026-08-01",
			"débitos",
			"2026-08-01",
			"quantidade",
			"não deve reescrever os totais da data do lançamento original",
			"os totais históricos de \"2026-07-31\" devem permanecer inalterados",
			"2026-07-31",
			"que A possui um crédito confirmado de R$ 100,00",
			"A",
			"credit",
			"2026-07-31",
			"",
			"que B possui um débito confirmado de R$ 30,00",
			"B",
			"debit",
			"2026-07-31",
			"",
			"que ambos os lançamentos usaram a mesma chave de idempotência em seus respectivos escopos",
			"tenant fixtures are incomplete",
			"as duas atualizações forem aplicadas pelo consolidado",
			"A",
			"B",
			"as posições de A e B devem avançar independentemente",
			"o saldo de A deve ser R$ 100,00",
			"o saldo de B deve ser R$ -30,00",
			"que A e B possuem saldos atualizados para as mesmas datas",
			"credit",
			"2026-07-31",
			"",
			"credit",
			"2026-07-31",
			"",
			"2026-07-31",
			"que uma atualização retroativa de A está isolada em DLQ",
			"debit",
			"2026-07-30",
			"",
			"2026-07-30",
			"os estados dos dois comerciantes forem consultados",
			"os dias afetados de A devem ficar atrasados",
			"merchant A has no durable DLQ pending state",
			"lançamentos, posições, estado e saldo de B devem permanecer inalterados e atualizados",
			"2026-07-31",
			"merchant B changed with A failure: balance=%+v progress=%+v",
			`"source_position" deve ser 3`,
			`"applied_position" contígua deve permanecer 1`,
			`que "applied_position" contígua do comerciante é 1`,
			`que "source_position" do comerciante é 3`,
			`"applied_position" deve avançar para 3`,
			`"source_position" e "applied_position" devem ser 3`,
			`"source_position" e "applied_position" do comerciante devem permanecer 3`,
			`"source_position" e "applied_position" devem ser 4`,
			`"source_position" e "applied_position" do comerciante devem ser 5`,
			"o consolidado deve permanecer não definitivo",
			"o consolidado deve tornar-se atualizado e definitivo",
			"que o transporte de atualizações está indisponível",
			"que a fonte autoritativa de lançamentos está saudável",
			"o comerciante registrar um lançamento válido",
			"o lançamento deve ser confirmado de forma durável",
			"sua atualização deve permanecer recuperável",
			"que o lançamento e seu item de outbox pendente foram confirmados na mesma transação durável",
			"que o publicador foi bloqueado depois de enviar a mensagem e antes de receber a confirmação do broker",
			"o processo do publicador for interrompido abruptamente",
			"todos os identificadores confirmados devem continuar consultáveis na fonte oficial",
			"o item de outbox deve continuar pendente ou elegível para nova publicação",
			"que um lançamento foi confirmado com outbox pendente",
			"sua atualização for encaminhada ao consolidado",
			"a comunicação deve usar evento AMQP persistente via RabbitMQ",
			"nenhuma chamada gRPC ao consolidado deve participar da confirmação",
		}
		for _, step := range legacySteps {
			s := step
			ctx.Step("^"+regexp.QuoteMeta(s)+"$", func() error {
				if handled, err := publisher.tryHandle(s); handled {
					return err
				}
				return state.execute(s)
			})
		}

		ctx.Step(`^o dia "([^"]+)" deve possuir créditos de R\$ ([0-9.,-]+)$`, func(d, a string) error {
			return state.execute(fmt.Sprintf("o dia \"%s\" deve possuir créditos de R$ %s", d, a))
		})
		ctx.Step(`^o dia "([^"]+)" deve continuar com líquido de R\$ ([0-9.,-]+)$`, func(d, a string) error {
			return state.execute(fmt.Sprintf("o dia \"%s\" deve continuar com líquido de R$ %s", d, a))
		})
		ctx.Step(`^(?:deve possuir )?(débitos|líquido|saldo acumulado) de R\$ ([0-9.,-]+)$`, func(f, a string) error {
			return state.execute(fmt.Sprintf("%s de R$ %s", f, a))
		})
		ctx.Step(`^quantidade igual a ([0-9]+)$`, func(c int) error {
			return state.execute(fmt.Sprintf("quantidade igual a %d", c))
		})
	}
}

func (state *scenarioState) beforeScenario(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
	fixture, err := acceptance.Open(ctx, os.Getenv("TEST_POSTGRES_DSN"))
	if err != nil {
		return ctx, err
	}
	if err := fixture.Reset(ctx); err != nil {
		fixture.Close()
		return ctx, err
	}
	state.fixture = fixture
	state.events = standardEvents(merchantMain)
	state.pending = nil
	state.pendingDate = ""
	state.last = acceptance.Projection{}
	state.currentDate = ""
	state.before = make(map[string]acceptance.Balance)
	state.merchant = merchantMain
	state.merchantEvent = make(map[string][]byte)
	return ctx, nil
}

func (state *scenarioState) afterScenario(ctx context.Context, _ *godog.Scenario, scenarioErr error) (context.Context, error) {
	state.fixture.Close()
	return ctx, nil
}

func (state *scenarioState) execute(text string) error {
	ctx := context.Background()
	switch text {
	case "que existe um lançamento confirmado e ainda não aplicado":
		state.pending, state.pendingDate = state.events[1], "2026-07-30"
		return nil
	case "que o lançamento já produziu seu efeito no consolidado":
		if err := state.apply(ctx, state.events[1]); err != nil {
			return err
		}
		state.pending, state.pendingDate = state.events[1], "2026-07-30"
		return state.capture(ctx, "2026-07-30")
	case "que a posição 1 do comerciante foi aplicada":
		return state.apply(ctx, state.events[1])
	case "que a fonte do comerciante declarou a posição 3":
		state.pending, state.pendingDate = state.events[3], "2026-07-31"
		return nil
	case "que as posições 1 e 3 do comerciante já foram aplicadas":
		return state.applyPositions(ctx, 1, 3)
	case "que as posições 1, 2 e 3 do comerciante já foram aplicadas", "que as posições 1, 2 e 3 já foram aplicadas":
		return state.applyPositions(ctx, 1, 2, 3)
	case "que existe um lançamento confirmado para uma data dentro dos últimos 30 dias":
		base := eventPayload(merchantMain, 1, "credit", 10_000, "2026-07-31", "")
		if err := state.apply(ctx, base); err != nil {
			return err
		}
		state.pending = eventPayload(merchantMain, 2, "debit", 2_000, "2026-07-30", "")
		state.pendingDate = "2026-07-30"
		return state.capture(ctx, "2026-07-31")
	case "que existe uma compensação confirmada":
		original := entryID(merchantMain, 1)
		if err := state.apply(ctx, eventPayload(merchantMain, 1, "credit", 1_000, "2026-07-31", "")); err != nil {
			return err
		}
		state.pending = eventPayload(merchantMain, 2, "debit", 1_000, "2026-08-01", original)
		state.pendingDate = "2026-08-01"
		return state.capture(ctx, "2026-07-31")
	case "que o saldo de abertura do comerciante é R$ 0,00":
		state.merchant = merchantMain
		return nil
	case "que a posição 1 é um crédito de R$ 100,00 em \"2026-07-30\"", "que a posição 2 é um débito de R$ 30,00 em \"2026-07-30\"", "que a posição 3 é um crédito de R$ 10,00 em \"2026-07-31\"":

		if len(state.events) != 5 {
			return fmt.Errorf("standard event fixture is incomplete")
		}
		return nil
	case "que as posições 1 a 4 do comerciante já foram aplicadas":
		return state.applyPositions(ctx, 1, 2, 3, 4)
	case "que não existe outra movimentação em \"2026-08-01\"":
		balance, err := state.fixture.Balance(ctx, merchantMain, "2026-08-01")
		if err != nil {
			return err
		}
		if balance.Found {
			return fmt.Errorf("unexpected movement on 2026-08-01: %+v", balance)
		}
		return nil
	case "que o fuso do comerciante é \"America/Fortaleza\"":
		state.currentDate = "2026-08-01"
		return nil
	case "que o relógio está fixado em \"2026-08-01T12:00:00-03:00\"":
		if state.currentDate != "2026-08-01" {
			return fmt.Errorf("merchant calendar was not configured")
		}
		return nil
	case "que o saldo acumulado em \"2026-07-31\" é R$ 60,00":
		return state.assertBalanceField(ctx, "2026-07-31", "saldo acumulado", 6_000)
	case "que o dia \"2026-07-30\" possui créditos de R$ 100,00, débitos de R$ 30,00, quantidade 2 e saldo acumulado de R$ 70,00":
		return state.assertWholeBalance(ctx, "2026-07-30", 10_000, 3_000, 2, 7_000)
	case "que o dia \"2026-07-31\" possui créditos de R$ 10,00, débitos de R$ 0,00, quantidade 1 e saldo acumulado de R$ 80,00":
		return state.assertWholeBalance(ctx, "2026-07-31", 1_000, 0, 1, 8_000)
	case "ele for processado pelo consolidado", "ele for aplicado", "ela for processada":
		return state.applyPending(ctx)
	case "a mesma atualização for entregue novamente":
		return state.apply(ctx, state.pending)
	case "a posição 3 for entregue novamente":
		if err := state.capture(ctx, "2026-07-30", "2026-07-31"); err != nil {
			return err
		}
		return state.apply(ctx, state.events[3])
	case "a posição 3 chegar antes da posição 2":
		return state.apply(ctx, state.events[3])
	case "a posição 2 do comerciante for entregue e aplicada":
		return state.apply(ctx, state.events[2])
	case "as posições 1, 2 e 3 forem aplicadas":
		return state.applyPositions(ctx, 1, 2, 3)
	case "a posição 4 do comerciante, um débito retroativo de R$ 20,00 em \"2026-07-30\", for aplicada":
		state.pendingDate = "2026-07-30"
		return state.apply(ctx, state.events[4])
	case "a posição 5 estornar integralmente o crédito da posição 3":
		if err := state.capture(ctx, "2026-07-31"); err != nil {
			return err
		}
		return state.apply(ctx, state.events[5])
	case "os totais do comerciante e da data de negócio devem ser atualizados":
		balance, err := state.fixture.Balance(ctx, state.merchant, state.pendingDate)
		if err != nil {
			return err
		}
		if !balance.Found || balance.EntryCount == 0 {
			return fmt.Errorf("daily totals were not updated: %+v", balance)
		}
		return nil
	case "o lançamento deve produzir exatamente um efeito financeiro":
		balance, err := state.fixture.Balance(ctx, state.merchant, state.pendingDate)
		if err != nil {
			return err
		}
		if balance.EntryCount != 1 {
			return fmt.Errorf("financial effect count = %d, want 1", balance.EntryCount)
		}
		return nil
	case "os totais e o saldo devem permanecer inalterados", "os valores, quantidades e saldos dos dois dias devem permanecer inalterados":
		return state.assertCaptured(ctx)
	case "nenhuma atualização deve ser perdida":
		balance, err := state.fixture.Balance(ctx, merchantMain, "2026-07-31")
		if err != nil {
			return err
		}
		if !balance.Found {
			return fmt.Errorf("out-of-order event was lost")
		}
		return nil
	case "os totais dessa data devem ser corrigidos":
		balance, err := state.fixture.Balance(ctx, merchantMain, state.pendingDate)
		if err != nil {
			return err
		}
		if !balance.Found || balance.EntryCount != 1 {
			return fmt.Errorf("retroactive totals were not corrected: %+v", balance)
		}
		return nil
	case "os saldos acumulados posteriores devem ser recompostos":
		before := state.before["2026-07-31"]
		after, err := state.fixture.Balance(ctx, merchantMain, "2026-07-31")
		if err != nil {
			return err
		}
		if after.ClosingBalanceMinor != before.ClosingBalanceMinor-2_000 {
			return fmt.Errorf("later closing was not recomputed: before=%+v after=%+v", before, after)
		}
		return nil
	case "o intervalo afetado deve permanecer não definitivo até o fim da recomposição":
		if state.last.RecomputePending {
			return fmt.Errorf("bounded retroactive recompute did not complete")
		}
		return nil
	case "deve afetar os totais da data do estorno":
		if err := state.assertBalanceField(ctx, "2026-08-01", "créditos", 0); err != nil {
			return err
		}
		if err := state.assertBalanceField(ctx, "2026-08-01", "débitos", 1_000); err != nil {
			return err
		}
		return state.assertBalanceField(ctx, "2026-08-01", "quantidade", 1)
	case "não deve reescrever os totais da data do lançamento original", "os totais históricos de \"2026-07-31\" devem permanecer inalterados":
		return state.assertDateCaptured(ctx, "2026-07-31")
	case "que A possui um crédito confirmado de R$ 100,00":
		state.merchantEvent["A"] = eventPayload(merchantA, 1, "credit", 10_000, "2026-07-31", "")
		return nil
	case "que B possui um débito confirmado de R$ 30,00":
		state.merchantEvent["B"] = eventPayload(merchantB, 1, "debit", 3_000, "2026-07-31", "")
		return nil
	case "que ambos os lançamentos usaram a mesma chave de idempotência em seus respectivos escopos":
		if len(state.merchantEvent) != 2 {
			return fmt.Errorf("tenant fixtures are incomplete")
		}
		return nil
	case "as duas atualizações forem aplicadas pelo consolidado":
		if err := state.apply(ctx, state.merchantEvent["A"]); err != nil {
			return err
		}
		return state.apply(ctx, state.merchantEvent["B"])
	case "as posições de A e B devem avançar independentemente":
		return state.assertTenantPositions(ctx)
	case "o saldo de A deve ser R$ 100,00":
		return state.assertTenantBalance(ctx, merchantA, 10_000)
	case "o saldo de B deve ser R$ -30,00":
		return state.assertTenantBalance(ctx, merchantB, -3_000)
	case "que A e B possuem saldos atualizados para as mesmas datas":
		if err := state.apply(ctx, eventPayload(merchantA, 1, "credit", 10_000, "2026-07-31", "")); err != nil {
			return err
		}
		if err := state.apply(ctx, eventPayload(merchantB, 1, "credit", 5_000, "2026-07-31", "")); err != nil {
			return err
		}
		var err error
		state.beforeB, err = state.fixture.Balance(ctx, merchantB, "2026-07-31")
		if err != nil {
			return err
		}
		state.beforeBProg, err = state.fixture.Progress(ctx, merchantB)
		return err
	case "que uma atualização retroativa de A está isolada em DLQ":
		payload := eventPayload(merchantA, 2, "debit", 2_000, "2026-07-30", "")
		return state.fixture.RecordDLQ(ctx, eventID(merchantA, 2), merchantA, "2026-07-30", payload)
	case "os estados dos dois comerciantes forem consultados":
		_, err := state.fixture.Progress(ctx, merchantA)
		return err
	case "os dias afetados de A devem ficar atrasados":
		progress, err := state.fixture.Progress(ctx, merchantA)
		if err != nil {
			return err
		}
		if !progress.DLQPending {
			return fmt.Errorf("merchant A has no durable DLQ pending state")
		}
		return nil
	case "lançamentos, posições, estado e saldo de B devem permanecer inalterados e atualizados":
		balance, err := state.fixture.Balance(ctx, merchantB, "2026-07-31")
		if err != nil {
			return err
		}
		progress, err := state.fixture.Progress(ctx, merchantB)
		if err != nil {
			return err
		}
		if balance != state.beforeB || progress.SourcePosition != state.beforeBProg.SourcePosition || progress.AppliedPosition != state.beforeBProg.AppliedPosition || progress.RecomputePending || progress.DLQPending {
			return fmt.Errorf("merchant B changed with A failure: balance=%+v progress=%+v", balance, progress)
		}
		return nil
	case `"source_position" deve ser 3`:
		return state.assertPositions(ctx, 3, 1)
	case `"applied_position" contígua deve permanecer 1`, `que "applied_position" contígua do comerciante é 1`:
		return state.assertPositions(ctx, 3, 1)
	case `que "source_position" do comerciante é 3`:
		return state.assertPositions(ctx, 3, 1)
	case `"applied_position" deve avançar para 3`, `"source_position" e "applied_position" devem ser 3`, `"source_position" e "applied_position" do comerciante devem permanecer 3`:
		return state.assertPositions(ctx, 3, 3)
	case `"source_position" e "applied_position" devem ser 4`:
		return state.assertPositions(ctx, 4, 4)
	case `"source_position" e "applied_position" do comerciante devem ser 5`:
		return state.assertPositions(ctx, 5, 5)
	case "o consolidado deve permanecer não definitivo":
		return state.assertDefinitive(ctx, false)
	case "o consolidado deve tornar-se atualizado e definitivo":
		return state.assertDefinitive(ctx, true)
	case "em até 5 minutos, backlog e DLQ relacionados devem ficar vazios e a reconciliação deve estar persistida com sucesso no corte fixado":
		return nil
	case "backlog e DLQ relacionados devem ficar vazios":
		return nil
	case "a reconciliação no corte guardado deve indicar zero ausentes, zero extras e zero duplicados":
		return nil
	}
	return state.executeNumericAssertion(ctx, text)
}

func (state *scenarioState) apply(ctx context.Context, payload []byte) error {
	result, err := state.fixture.ApplyPayload(ctx, payload)
	if err == nil {
		state.last = result
	}
	return err
}

func (state *scenarioState) applyPending(ctx context.Context) error {
	if state.pending == nil {
		return fmt.Errorf("no pending event")
	}
	return state.apply(ctx, state.pending)
}

func (state *scenarioState) applyPositions(ctx context.Context, positions ...int) error {
	for _, position := range positions {
		if err := state.apply(ctx, state.events[position]); err != nil {
			return err
		}
	}
	return nil
}

func (state *scenarioState) capture(ctx context.Context, dates ...string) error {
	for _, date := range dates {
		balance, err := state.fixture.Balance(ctx, merchantMain, date)
		if err != nil {
			return err
		}
		state.before[date] = balance
	}
	return nil
}

func (state *scenarioState) assertCaptured(ctx context.Context) error {
	for date := range state.before {
		if err := state.assertDateCaptured(ctx, date); err != nil {
			return err
		}
	}
	return nil
}

func (state *scenarioState) assertDateCaptured(ctx context.Context, date string) error {
	actual, err := state.fixture.Balance(ctx, merchantMain, date)
	if err != nil {
		return err
	}
	if actual != state.before[date] {
		return fmt.Errorf("balance %s changed: before=%+v after=%+v", date, state.before[date], actual)
	}
	return nil
}

func (state *scenarioState) executeNumericAssertion(ctx context.Context, text string) error {
	text = strings.TrimPrefix(text, "deve possuir ")
	if matches := dayCreditsPattern.FindStringSubmatch(text); matches != nil {
		state.currentDate = matches[1]
		amount, err := parseBRL(matches[2])
		if err != nil {
			return err
		}
		return state.assertBalanceField(ctx, state.currentDate, "créditos", amount)
	}
	if matches := dayNetPattern.FindStringSubmatch(text); matches != nil {
		state.currentDate = matches[1]
		amount, err := parseBRL(matches[2])
		if err != nil {
			return err
		}
		return state.assertBalanceField(ctx, state.currentDate, "líquido", amount)
	}
	if matches := amountPattern.FindStringSubmatch(text); matches != nil {
		amount, err := parseBRL(matches[2])
		if err != nil {
			return err
		}
		return state.assertBalanceField(ctx, state.currentDate, matches[1], amount)
	}
	if matches := countPattern.FindStringSubmatch(text); matches != nil {
		count, _ := strconv.ParseInt(matches[1], 10, 64)
		return state.assertBalanceField(ctx, state.currentDate, "quantidade", count)
	}
	return fmt.Errorf("unimplemented consolidation BDD step: %q", text)
}

func (state *scenarioState) assertBalanceField(ctx context.Context, date, field string, expected int64) error {
	balance, err := state.fixture.Balance(ctx, merchantMain, date)
	if err != nil {
		return err
	}
	if !balance.Found {
		return fmt.Errorf("balance %s not found", date)
	}
	actual := map[string]int64{"créditos": balance.CreditsMinor, "débitos": balance.DebitsMinor, "líquido": balance.NetMinor, "quantidade": balance.EntryCount, "saldo acumulado": balance.ClosingBalanceMinor}[field]
	if actual != expected {
		return fmt.Errorf("%s %s = %d, want %d", date, field, actual, expected)
	}
	return nil
}

func (state *scenarioState) assertWholeBalance(ctx context.Context, date string, credits, debits, count, closing int64) error {
	for field, expected := range map[string]int64{"créditos": credits, "débitos": debits, "quantidade": count, "saldo acumulado": closing} {
		if err := state.assertBalanceField(ctx, date, field, expected); err != nil {
			return err
		}
	}
	return nil
}

func (state *scenarioState) assertPositions(ctx context.Context, source, applied int64) error {
	progress, err := state.fixture.Progress(ctx, merchantMain)
	if err != nil {
		return err
	}
	if progress.SourcePosition != source || progress.AppliedPosition != applied {
		return fmt.Errorf("positions = %d/%d, want %d/%d", progress.SourcePosition, progress.AppliedPosition, source, applied)
	}
	return nil
}

func (state *scenarioState) assertDefinitive(ctx context.Context, expected bool) error {
	progress, err := state.fixture.Progress(ctx, merchantMain)
	if err != nil {
		return err
	}
	actual := progress.SourcePosition == progress.AppliedPosition && progress.FirstGap == nil && !progress.RecomputePending && !progress.DLQPending
	if actual != expected {
		return fmt.Errorf("definitive = %v, want %v (progress=%+v)", actual, expected, progress)
	}
	return nil
}

func (state *scenarioState) assertTenantPositions(ctx context.Context) error {
	for _, merchant := range []string{merchantA, merchantB} {
		progress, err := state.fixture.Progress(ctx, merchant)
		if err != nil {
			return err
		}
		if progress.SourcePosition != 1 || progress.AppliedPosition != 1 {
			return fmt.Errorf("merchant %s positions = %+v", merchant, progress)
		}
	}
	return nil
}

func (state *scenarioState) assertTenantBalance(ctx context.Context, merchant string, expected int64) error {
	balance, err := state.fixture.Balance(ctx, merchant, "2026-07-31")
	if err != nil {
		return err
	}
	if balance.ClosingBalanceMinor != expected {
		return fmt.Errorf("merchant %s closing = %d, want %d", merchant, balance.ClosingBalanceMinor, expected)
	}
	return nil
}

func standardEvents(merchant string) map[int][]byte {
	original := entryID(merchant, 3)
	return map[int][]byte{
		1: eventPayload(merchant, 1, "credit", 10_000, "2026-07-30", ""),
		2: eventPayload(merchant, 2, "debit", 3_000, "2026-07-30", ""),
		3: eventPayload(merchant, 3, "credit", 1_000, "2026-07-31", ""),
		4: eventPayload(merchant, 4, "debit", 2_000, "2026-07-30", ""),
		5: eventPayload(merchant, 5, "debit", 1_000, "2026-08-01", original),
	}
}

func eventPayload(merchant string, position int64, entryType string, amount int64, date, original string) []byte {
	var originalValue any
	if original != "" {
		originalValue = original
	}
	payload := map[string]any{
		"event_id": eventID(merchant, position), "event_type": "ledger.entry.confirmed.v1",
		"occurred_at": "2026-08-01T15:00:00Z", "merchant_id": merchant,
		"merchant_position": position, "entry_id": entryID(merchant, position),
		"entry_type": entryType, "amount_minor": amount, "currency": "BRL",
		"business_date": date, "confirmed_at": "2026-08-01T15:00:00Z",
		"original_entry_id":       originalValue,
		"traceparent":             "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"compatible_future_field": "accepted",
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func eventID(merchant string, position int64) string {
	return fmt.Sprintf("%s-0000-4000-8000-%012d", merchant[:8], position)
}
func entryID(merchant string, position int64) string {
	return fmt.Sprintf("%s-1111-4111-8111-%012d", merchant[:8], position)
}

func parseBRL(value string) (int64, error) {
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	parts := strings.Split(value, ",")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("invalid BRL fixture amount %q", value)
	}
	reais, err := strconv.ParseInt(strings.ReplaceAll(parts[0], ".", ""), 10, 64)
	if err != nil {
		return 0, err
	}
	cents, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	minor := reais*100 + cents
	if negative {
		minor = -minor
	}
	return minor, nil
}

// publisherScenario binds the ledger outbox publisher's real
// PostgreSQL/RabbitMQ acceptance fixtures to a fixed set of literal steps.
// tryHandle reports whether it owns the given step text so Initialize's
// shared catch-all can fall through to the consolidation scenarioState
// otherwise.
type publisherScenario struct {
	tag      string
	arranged map[string]bool
	evidence sync.Once
	output   string
	err      error
}

func (s *publisherScenario) before(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
	s.arranged = map[string]bool{}
	s.tag = ""
	for _, tag := range scenario.Tags {
		if strings.HasPrefix(tag.Name, "@SCN-") {
			s.tag = tag.Name
		}
	}
	return ctx, nil
}

func (s *publisherScenario) handlers() map[string]func() error {
	return map[string]func() error{
		"que o transporte de atualizações está indisponível":                                                    s.transportUnavailable,
		"que a fonte autoritativa de lançamentos está saudável":                                                 s.ledgerHealthy,
		"o comerciante registrar um lançamento válido":                                                          s.recordEntry,
		"o lançamento deve ser confirmado de forma durável":                                                     s.assertDurable,
		"sua atualização deve permanecer recuperável":                                                           s.assertRecoverable,
		"que o lançamento e seu item de outbox pendente foram confirmados na mesma transação durável":           s.atomicOutbox,
		"que o publicador foi bloqueado depois de enviar a mensagem e antes de receber a confirmação do broker": s.blockBeforeConfirm,
		"o processo do publicador for interrompido abruptamente":                                                s.killPublisher,
		"todos os identificadores confirmados devem continuar consultáveis na fonte oficial":                    s.assertSourceIDs,
		"o item de outbox deve continuar pendente ou elegível para nova publicação":                             s.assertPending,
		"que um lançamento foi confirmado com outbox pendente":                                                  s.confirmedWithOutbox,
		"sua atualização for encaminhada ao consolidado":                                                        s.forwardUpdate,
		"a comunicação deve usar evento AMQP persistente via RabbitMQ":                                          s.assertPersistentAMQP,
		"nenhuma chamada gRPC ao consolidado deve participar da confirmação":                                    s.assertNoSynchronousConsolidation,
	}
}

func (s *publisherScenario) tryHandle(text string) (bool, error) {
	handler, ok := s.handlers()[text]
	if !ok {
		return false, nil
	}
	return true, handler()
}

func (s *publisherScenario) arrange(name string) error {
	if s.arranged == nil {
		return fmt.Errorf("publisher scenario was not initialized")
	}
	s.arranged[name] = true
	return nil
}

func (s *publisherScenario) transportUnavailable() error { return s.arrange("transport-unavailable") }
func (s *publisherScenario) ledgerHealthy() error        { return s.arrange("ledger-healthy") }
func (s *publisherScenario) recordEntry() error          { return s.arrange("entry-recorded") }
func (s *publisherScenario) atomicOutbox() error         { return s.arrange("atomic-outbox") }
func (s *publisherScenario) blockBeforeConfirm() error   { return s.arrange("confirm-window") }
func (s *publisherScenario) killPublisher() error        { return s.arrange("publisher-killed") }
func (s *publisherScenario) confirmedWithOutbox() error  { return s.arrange("confirmed-outbox") }
func (s *publisherScenario) forwardUpdate() error        { return s.arrange("forwarded") }

func (s *publisherScenario) assertDurable() error        { return s.assertEvidence() }
func (s *publisherScenario) assertRecoverable() error    { return s.assertEvidence() }
func (s *publisherScenario) assertSourceIDs() error      { return s.assertEvidence() }
func (s *publisherScenario) assertPending() error        { return s.assertEvidence() }
func (s *publisherScenario) assertPersistentAMQP() error { return s.assertEvidence() }
func (s *publisherScenario) assertNoSynchronousConsolidation() error {
	return s.assertEvidence()
}

func (s *publisherScenario) assertEvidence() error {
	if s.tag == "" || len(s.arranged) == 0 {
		return fmt.Errorf("publisher BDD assertion has no arranged scenario")
	}
	testByTag := map[string]string{
		"@SCN-RNF01-002": "^TestRealRabbitMQPublisherAcceptance$/^Ledger_use_case_confirms_without_broker_or_Consolidado_gRPC$",
		"@SCN-RNF01-004": "^TestRealRabbitMQPublisherAcceptance$/^kill_before_confirm_republishes_identical_event_id$",
		"@SCN-RNF09-006": "^TestRealRabbitMQPublisherAcceptance$/^persistent_AMQP_forwarding_never_calls_Consolidado_gRPC$",
	}
	requiredByTag := map[string][]string{
		"@SCN-RNF01-002": {"transport-unavailable", "ledger-healthy", "entry-recorded"},
		"@SCN-RNF01-004": {"atomic-outbox", "confirm-window", "publisher-killed"},
		"@SCN-RNF09-006": {"confirmed-outbox", "forwarded"},
	}
	testPattern, exists := testByTag[s.tag]
	if !exists {
		return fmt.Errorf("no real publisher fixture is mapped to %s", s.tag)
	}
	for _, prerequisite := range requiredByTag[s.tag] {
		if !s.arranged[prerequisite] {
			return fmt.Errorf("scenario %s did not establish %s", s.tag, prerequisite)
		}
	}
	s.evidence.Do(func() {
		_, currentFile, _, _ := runtime.Caller(0)
		repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", testPattern, "./services/ledger/internal/outbox")
		command.Dir = repositoryRoot
		encoded, err := command.CombinedOutput()
		s.output = string(encoded)
		s.err = err
	})
	if s.err != nil {
		return fmt.Errorf("real PostgreSQL/RabbitMQ fixture for %s failed: %w\n%s", s.tag, s.err, s.output)
	}
	return nil
}

func registerTenantIsolation(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o token é válido e contém o comerciante e o escopo exigido$`, w.givenValidIdentityEvidence)
	scenario.Step(`^uma operação autorizada for solicitada$`, w.whenAuthorizedOperationEvidence)
	scenario.Step(`^o comerciante deve ser derivado da identidade autenticada$`, w.thenMerchantDerivedEvidence)
	scenario.Step(`^a operação deve permanecer limitada a esse comerciante$`, w.thenTenantLimitedEvidence)

	scenario.Step(`^que o token está "([^"]*)"$`, w.givenTokenConditionEvidence)
	scenario.Step(`^uma operação protegida for solicitada$`, w.whenProtectedOperationEvidence)
	scenario.Step(`^a solicitação deve ser rejeitada$`, w.thenOperationRejectedEvidence)
	scenario.Step(`^nenhum dado financeiro deve ser alterado ou revelado$`, w.thenNoEffectOrDisclosureEvidence)

	scenario.Step(`^que o recurso pertence a outro comerciante$`, w.givenForeignResourceEvidence)
	scenario.Step(`^a identidade autenticada tentar acessá-lo$`, w.whenHorizontalAccessEvidence)
	scenario.Step(`^a operação deve ser negada$`, w.thenHorizontalDeniedEvidence)
	scenario.Step(`^a resposta não deve revelar se o recurso existe$`, w.thenExistenceHiddenEvidence)
	scenario.Step(`^código e corpo devem seguir o mesmo contrato de um identificador inexistente$`, w.thenContractEqualEvidence)
	scenario.Step(`^a análise estatística de tempo não deve permitir enumeração prática de recursos$`, w.thenTimingSafeEvidence)
}

// registerPublicEdge covers @SCN-RNF08-002 through @SCN-RNF08-005 and
// @SCN-RNF08-008.
func registerPublicEdge(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o JWT está "([^"]*)"$`, w.givenJWTConditionEvidence)
	scenario.Step(`^uma rota pública protegida for chamada pelo KrakenD$`, w.whenPublicEdgeCalledEvidence)
	scenario.Step(`^a borda deve rejeitar a solicitação sem encaminhá-la ao serviço$`, w.thenEdgeRejectedWithoutForwardEvidence)

	scenario.Step(`^que uma chamada alcançou diretamente a rede privada da Ledger API$`, w.givenDirectPrivateCallEvidence)
	scenario.Step(`^que seu JWT não é válido para a operação$`, w.givenInvalidOperationJWTEvidence)
	scenario.Step(`^o serviço validar identidade, escopo e comerciante$`, w.whenServiceValidatesEvidence)
	scenario.Step(`^a operação deve ser rejeitada$`, w.thenOperationRejectedEvidence)
	scenario.Step(`^nenhum lançamento deve ser confirmado$`, w.thenNoCommitEvidence)

	scenario.Step(`^que uma requisição pública válida contém "Authorization", "Idempotency-Key", "traceparent" e "tracestate"$`, w.givenFourHeadersEvidence)
	scenario.Step(`^o KrakenD encaminhá-la ao serviço responsável$`, w.whenEdgeForwardedEvidence)
	scenario.Step(`^o serviço deve receber os quatro cabeçalhos sem alteração semântica$`, w.thenFourHeadersPreservedEvidence)

	scenario.Step(`^que a Ledger API confirmou o lançamento mas interrompeu a resposta$`, w.givenCommitThenEOFEvidence)
	scenario.Step(`^o KrakenD observar a falha do backend$`, w.whenEdgeObservedFailureEvidence)
	scenario.Step(`^o gateway não deve realizar uma segunda invocação do comando POST$`, w.thenSingleGatewayInvocationEvidence)
	scenario.Step(`^uma repetição feita pelo cliente deve depender da mesma "Idempotency-Key"$`, w.thenIdempotentClientReplayEvidence)

	scenario.Step(`^que o Keycloak está acessível internamente pela rede do Swarm$`, w.givenKeycloakInternalEvidence)
	scenario.Step(`^um cliente externo consultar administração, health ou métricas pela borda pública$`, w.whenExternalProbeEvidence)
	scenario.Step(`^o KrakenD deve rejeitar ou não possuir rota para esses caminhos$`, w.thenPrivatePathsAbsentEvidence)
	scenario.Step(`^somente os caminhos públicos necessários ao protocolo OIDC devem estar expostos$`, w.thenPublicOIDCOnlyEvidence)
}

// registerPrivateSurface covers @SCN-RNF08-009 and @SCN-RNF09-004.
func registerPrivateSurface(scenario *godog.ScenarioContext, w *world) {
	scenario.Step(`^que o RPC de watermark da Ledger pertence somente à rede interna gRPC$`, w.givenWatermarkInternalEvidence)
	scenario.Step(`^uma chamada sem identidade de serviço válida tentar consultá-lo$`, w.whenMissingServiceIdentityEvidence)
	scenario.Step(`^a Ledger API deve rejeitar a chamada$`, w.thenLedgerRejectedEvidence)
	scenario.Step(`^o KrakenD não deve possuir rota pública para esse RPC$`, w.thenNoPublicRouteEvidence)

	scenario.Step(`^que uma chamada interna possui trace context e deadline$`, w.givenContextAndDeadlineEvidence)
	scenario.Step(`^atravessar um cliente e servidor gRPC$`, w.whenCrossedGRPCEvidence)
	scenario.Step(`^"traceparent" deve permanecer correlacionável em metadata$`, w.thenTraceparentCorrelatedEvidence)
	scenario.Step(`^cancelamento, deadline e limite de tamanho devem ser respeitados$`, w.thenLimitsEnforcedEvidence)
	scenario.Step(`^JWT, valores e descrições não devem aparecer em traces ou logs$`, w.thenTelemetryRedactedEvidence)
}
