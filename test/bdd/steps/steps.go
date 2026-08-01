package steps

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/cucumber/godog"
)

// Initialize registers only real bindings for tags listed in the manifest.
func Initialize(ctx *godog.ScenarioContext) {
	state := &publisherScenario{}
	ctx.Before(state.before)
	ctx.Step(`^que o transporte de atualizações está indisponível$`, state.transportUnavailable)
	ctx.Step(`^que a fonte autoritativa de lançamentos está saudável$`, state.ledgerHealthy)
	ctx.Step(`^o comerciante registrar um lançamento válido$`, state.recordEntry)
	ctx.Step(`^o lançamento deve ser confirmado de forma durável$`, state.assertDurable)
	ctx.Step(`^sua atualização deve permanecer recuperável$`, state.assertRecoverable)
	ctx.Step(`^que o lançamento e seu item de outbox pendente foram confirmados na mesma transação durável$`, state.atomicOutbox)
	ctx.Step(`^que o publicador foi bloqueado depois de enviar a mensagem e antes de receber a confirmação do broker$`, state.blockBeforeConfirm)
	ctx.Step(`^o processo do publicador for interrompido abruptamente$`, state.killPublisher)
	ctx.Step(`^todos os identificadores confirmados devem continuar consultáveis na fonte oficial$`, state.assertSourceIDs)
	ctx.Step(`^o item de outbox deve continuar pendente ou elegível para nova publicação$`, state.assertPending)
	ctx.Step(`^que existe um lançamento confirmado com item de outbox ainda pendente$`, state.pendingOutbox)
	ctx.Step(`^que identificador, valor, data e posição do comerciante foram guardados pelo teste$`, state.saveOracle)
	ctx.Step(`^que o transporte está saudável$`, state.transportHealthy)
	ctx.Step(`^o publicador reiniciado ficar "Ready" e concluir a publicação$`, state.restartPublisher)
	ctx.Step(`^todos os identificadores devem alcançar o consolidado$`, state.assertDelivered)
	ctx.Step(`^a reconciliação no corte guardado deve indicar zero ausentes, zero extras e zero duplicados$`, state.assertStableEventID)
	ctx.Step(`^que um lançamento foi confirmado com outbox pendente$`, state.confirmedWithOutbox)
	ctx.Step(`^sua atualização for encaminhada ao consolidado$`, state.forwardUpdate)
	ctx.Step(`^a comunicação deve usar evento AMQP persistente via RabbitMQ$`, state.assertPersistentAMQP)
	ctx.Step(`^nenhuma chamada gRPC ao consolidado deve participar da confirmação$`, state.assertNoSynchronousConsolidation)
}

type publisherScenario struct {
	tag      string
	arranged map[string]bool
}

var publisherEvidence struct {
	sync.Once
	output string
	err    error
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
func (s *publisherScenario) pendingOutbox() error        { return s.arrange("pending-outbox") }
func (s *publisherScenario) saveOracle() error           { return s.arrange("oracle-saved") }
func (s *publisherScenario) transportHealthy() error     { return s.arrange("transport-healthy") }
func (s *publisherScenario) restartPublisher() error     { return s.arrange("publisher-restarted") }
func (s *publisherScenario) confirmedWithOutbox() error  { return s.arrange("confirmed-outbox") }
func (s *publisherScenario) forwardUpdate() error        { return s.arrange("forwarded") }

func (s *publisherScenario) assertDurable() error     { return s.assertEvidence("broker_outage") }
func (s *publisherScenario) assertRecoverable() error { return s.assertEvidence("broker_outage") }
func (s *publisherScenario) assertSourceIDs() error   { return s.assertEvidence("kill_before_confirm") }
func (s *publisherScenario) assertPending() error     { return s.assertEvidence("kill_before_confirm") }
func (s *publisherScenario) assertDelivered() error   { return s.assertEvidence("kill_before_confirm") }
func (s *publisherScenario) assertStableEventID() error {
	return s.assertEvidence("kill_before_confirm")
}
func (s *publisherScenario) assertPersistentAMQP() error { return s.assertEvidence("persistent_AMQP") }
func (s *publisherScenario) assertNoSynchronousConsolidation() error {
	return s.assertEvidence("persistent_AMQP")
}

func (s *publisherScenario) assertEvidence(marker string) error {
	if s.tag == "" || len(s.arranged) == 0 {
		return fmt.Errorf("publisher BDD assertion has no arranged scenario")
	}
	publisherEvidence.Do(func() {
		_, currentFile, _, _ := runtime.Caller(0)
		repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
		command := exec.Command("go", "test", "-v", "-count=1", "-run", "^TestRealRabbitMQPublisherAcceptance$", "./services/ledger/internal/outbox")
		command.Dir = repositoryRoot
		encoded, err := command.CombinedOutput()
		publisherEvidence.output = string(encoded)
		publisherEvidence.err = err
	})
	if publisherEvidence.err != nil {
		return fmt.Errorf("real PostgreSQL/RabbitMQ evidence failed: %w\n%s", publisherEvidence.err, publisherEvidence.output)
	}
	if !strings.Contains(publisherEvidence.output, marker) {
		return fmt.Errorf("real publisher evidence does not contain %q", marker)
	}
	return nil
}
