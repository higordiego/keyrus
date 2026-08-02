package steps

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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
	ctx.Step(`^que um lançamento foi confirmado com outbox pendente$`, state.confirmedWithOutbox)
	ctx.Step(`^sua atualização for encaminhada ao consolidado$`, state.forwardUpdate)
	ctx.Step(`^a comunicação deve usar evento AMQP persistente via RabbitMQ$`, state.assertPersistentAMQP)
	ctx.Step(`^nenhuma chamada gRPC ao consolidado deve participar da confirmação$`, state.assertNoSynchronousConsolidation)
}

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
