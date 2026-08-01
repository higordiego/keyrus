package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	fullSHA       = regexp.MustCompile(`^[^@[:space:]]+@[0-9a-f]{40}$`)
	dockerDigest  = regexp.MustCompile(`^docker://[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
	hostedUbuntu  = regexp.MustCompile(`^ubuntu-(latest|[0-9]{2}\.[0-9]{2})$`)
	secretContext = regexp.MustCompile(`(?i)\bsecrets\s*(\.|\[)`)
	forbiddenRun  = regexp.MustCompile(`(?i)(docker\s+push|docker\s+stack\s+deploy|\bkubectl\b|\bhelm\s+(install|upgrade)|ghcr\.io|\baws\s+(ecs|eks|deploy)\b)`)
)

type violation struct {
	file    string
	message string
}

func (v violation) Error() string { return v.file + ": " + v.message }

func main() {
	paths, err := workflowFiles(flagArgs())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "no workflow YAML files found")
		os.Exit(2)
	}

	failed := false
	for _, path := range paths {
		if err := validateWorkflow(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func flagArgs() []string {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func workflowFiles(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, errors.New("usage: workflowpolicy WORKFLOW_OR_DIRECTORY [...]")
	}
	seen := map[string]bool{}
	var paths []string
	for _, input := range inputs {
		info, err := os.Stat(input)
		if err != nil {
			return nil, fmt.Errorf("workflow input %s: %w", input, err)
		}
		if !info.IsDir() {
			if !seen[input] {
				paths = append(paths, input)
				seen[input] = true
			}
			continue
		}
		err = filepath.WalkDir(input, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if (ext == ".yml" || ext == ".yaml") && !seen[path] {
				paths = append(paths, path)
				seen[path] = true
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk workflow directory %s: %w", input, err)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func validateWorkflow(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return violation{path, fmt.Sprintf("open workflow: %v", err)}
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return violation{path, fmt.Sprintf("parse YAML: %v", err)}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return violation{path, "multiple YAML documents are forbidden"}
		}
		return violation{path, fmt.Sprintf("parse trailing YAML: %v", err)}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return violation{path, "workflow root must be a mapping"}
	}
	root := document.Content[0]
	if err := validateYAMLShape(root); err != nil {
		return violation{path, err.Error()}
	}
	if err := rejectSecretContext(root); err != nil {
		return violation{path, err.Error()}
	}

	top := mapping(root)
	if trigger, ok := top["on"]; ok && trigger.Kind == yaml.MappingNode {
		if _, forbidden := mapping(trigger)["pull_request_target"]; forbidden {
			return violation{path, "pull_request_target is forbidden"}
		}
	}
	if err := exactPermissions(top["permissions"], map[string]string{"contents": "read"}); err != nil {
		return violation{path, "top-level permissions " + err.Error()}
	}
	if err := validateConcurrency(top["concurrency"]); err != nil {
		return violation{path, err.Error()}
	}
	jobsNode := top["jobs"]
	if jobsNode == nil || jobsNode.Kind != yaml.MappingNode || len(jobsNode.Content) == 0 {
		return violation{path, "jobs must be a non-empty mapping"}
	}

	jobs := mapping(jobsNode)
	jobIDs := make([]string, 0, len(jobs))
	for id := range jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	for _, jobID := range jobIDs {
		if err := validateJob(path, jobID, jobs[jobID]); err != nil {
			return violation{path, fmt.Sprintf("job %s: %v", jobID, err)}
		}
	}
	return nil
}

func validateYAMLShape(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are forbidden")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("mapping keys must be strings")
			}
			if key.Value == "<<" {
				return errors.New("YAML merge keys are forbidden")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML key %q is forbidden", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLShape(child); err != nil {
			return err
		}
	}
	return nil
}

func rejectSecretContext(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if strings.EqualFold(node.Value, "secrets") || secretContext.MatchString(node.Value) {
			return errors.New("secret context access is forbidden")
		}
	}
	for _, child := range node.Content {
		if err := rejectSecretContext(child); err != nil {
			return err
		}
	}
	return nil
}

func validateConcurrency(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return errors.New("concurrency must be a mapping")
	}
	values := mapping(node)
	group := values["group"]
	if group == nil || group.Kind != yaml.ScalarNode || strings.TrimSpace(group.Value) == "" {
		return errors.New("concurrency.group is required")
	}
	cancel := values["cancel-in-progress"]
	if cancel == nil || cancel.Tag != "!!bool" || cancel.Value != "true" {
		return errors.New("concurrency.cancel-in-progress must be true")
	}
	return nil
}

func validateJob(path, jobID string, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("definition must be a mapping")
	}
	job := mapping(node)
	if _, exists := job["environment"]; exists {
		return errors.New("environment is forbidden in this phase")
	}
	runner := job["runs-on"]
	if runner == nil || runner.Kind != yaml.ScalarNode || !hostedUbuntu.MatchString(runner.Value) {
		return errors.New("runs-on must be an explicit GitHub-hosted Ubuntu label")
	}
	timeout := job["timeout-minutes"]
	if timeout == nil || timeout.Tag != "!!int" {
		return errors.New("timeout-minutes must be a positive integer")
	}
	minutes, err := strconv.Atoi(timeout.Value)
	if err != nil || minutes <= 0 {
		return errors.New("timeout-minutes must be a positive integer")
	}

	if permissionNode, ok := job["permissions"]; ok {
		expected := map[string]string{"contents": "read"}
		authorization := filepath.Base(path) + "/" + jobID
		switch authorization {
		case "security.yml/sarif-upload":
			expected = map[string]string{"actions": "read", "contents": "read", "security-events": "write"}
		case "codeql.yml/analyze":
			expected = map[string]string{"contents": "read", "security-events": "write"}
		}
		if err := exactPermissions(permissionNode, expected); err != nil {
			return fmt.Errorf("permissions %w", err)
		}
	}

	steps := job["steps"]
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) == 0 {
		return errors.New("steps must be a non-empty sequence")
	}
	for index, stepNode := range steps.Content {
		if stepNode.Kind != yaml.MappingNode {
			return fmt.Errorf("step %d must be a mapping", index+1)
		}
		step := mapping(stepNode)
		if uses, ok := step["uses"]; ok {
			if uses.Kind != yaml.ScalarNode || !immutableAction(uses.Value) {
				return fmt.Errorf("step %d external action must use a full immutable digest/SHA: %s", index+1, uses.Value)
			}
		}
		if run, ok := step["run"]; ok && run.Kind == yaml.ScalarNode && forbiddenRun.MatchString(run.Value) {
			return fmt.Errorf("step %d contains a publication or deployment command", index+1)
		}
	}
	return nil
}

func exactPermissions(node *yaml.Node, expected map[string]string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return errors.New("must be an explicit mapping with least privilege")
	}
	actualNodes := mapping(node)
	if len(actualNodes) != len(expected) {
		return fmt.Errorf("must be exactly %s", formatPermissions(expected))
	}
	for key, value := range expected {
		actual := actualNodes[key]
		if actual == nil || actual.Kind != yaml.ScalarNode || actual.Value != value {
			return fmt.Errorf("must be exactly %s", formatPermissions(expected))
		}
	}
	return nil
}

func formatPermissions(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+":"+values[key])
	}
	return strings.Join(parts, ",")
}

func immutableAction(reference string) bool {
	return strings.HasPrefix(reference, "./") || fullSHA.MatchString(reference) || dockerDigest.MatchString(reference)
}

func mapping(node *yaml.Node) map[string]*yaml.Node {
	values := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		values[node.Content[index].Value] = node.Content[index+1]
	}
	return values
}
