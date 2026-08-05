package bdd_test

import (
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/formatters"
	messages "github.com/cucumber/messages/go/v34"
	"github.com/higordiegoti/keyrus/internal/bddcatalog"
)

const inventoryFormat = "t01-scenario-inventory"

var (
	inventoryRegisterOnce sync.Once
	inventoryPointerMu    sync.Mutex
	activeInventory       *scenarioInventory
)

type scenarioInventory struct {
	tags    map[string]struct{}
	pickles int
}

type inventoryFormatter struct {
	inventory *scenarioInventory
}

func (*inventoryFormatter) TestRunStarted()                                   {}
func (*inventoryFormatter) Feature(*messages.GherkinDocument, string, []byte) {}
func (formatter *inventoryFormatter) Pickle(pickle *messages.Pickle) {
	formatter.inventory.pickles++
	for _, tag := range pickle.Tags {
		if len(tag.Name) > len("@SCN-") && tag.Name[:len("@SCN-")] == "@SCN-" {
			formatter.inventory.tags[tag.Name] = struct{}{}
		}
	}
}
func (*inventoryFormatter) Defined(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition) {
}
func (*inventoryFormatter) Failed(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition, error) {
}
func (*inventoryFormatter) Passed(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition) {
}
func (*inventoryFormatter) Skipped(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition) {
}
func (*inventoryFormatter) Undefined(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition) {
}
func (*inventoryFormatter) Pending(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition) {
}
func (*inventoryFormatter) Ambiguous(*messages.Pickle, *messages.PickleStep, *formatters.StepDefinition, error) {
}
func (*inventoryFormatter) Summary() {}

func registerInventoryFormatter() {
	inventoryRegisterOnce.Do(func() {
		godog.Format(inventoryFormat, "collect unique scenario tags for catalog reconciliation", func(_ string, _ io.Writer) godog.Formatter {
			inventoryPointerMu.Lock()
			defer inventoryPointerMu.Unlock()
			return &inventoryFormatter{inventory: activeInventory}
		})
	})
}

func TestGodogDiscoveryMatchesCatalog(t *testing.T) {
	registerInventoryFormatter()
	catalog, err := bddcatalog.Load(featuresDir, manifest)
	if err != nil {
		t.Fatal(err)
	}

	inventory := &scenarioInventory{tags: make(map[string]struct{})}
	inventoryPointerMu.Lock()
	activeInventory = inventory
	inventoryPointerMu.Unlock()
	status := godog.TestSuite{
		Name:                "catalog-reconciliation",
		ScenarioInitializer: func(*godog.ScenarioContext) {},
		Options: &godog.Options{
			Format:   inventoryFormat,
			NoColors: true,
			Strict:   true,
			Paths:    []string{featuresDir},
			Output:   io.Discard,
		},
	}.Run()
	if status == 0 {
		t.Fatal("unbound discovery suite unexpectedly passed; missing bindings must fail")
	}
	if len(inventory.tags) != len(catalog.ScenarioTags) {
		t.Fatalf("Godog discovered %d unique @SCN tags, catalog has %d", len(inventory.tags), len(catalog.ScenarioTags))
	}
	if got, want := inventory.pickles, 95; got != want {
		t.Errorf("Godog discovered %d pickles, want %d expanded executions", got, want)
	}
	for _, tag := range catalog.ScenarioTags {
		if _, exists := inventory.tags[tag]; !exists {
			t.Errorf("catalog tag not discovered by Godog: %s", tag)
		}
	}
	for tag := range inventory.tags {
		found := false
		for _, catalogTag := range catalog.ScenarioTags {
			if catalogTag == tag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Godog discovered tag absent from catalog: %s", tag)
		}
	}
	if t.Failed() {
		t.Log(fmt.Sprintf("Godog inventory: %#v", inventory.tags))
	}
}
