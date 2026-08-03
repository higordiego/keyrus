package identityruntime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ParseAssignments(raw string) (map[string][]string, error) {
	result := make(map[string][]string)
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	for _, assignment := range strings.Split(raw, ";") {
		name, values, found := strings.Cut(strings.TrimSpace(assignment), "=")
		if !found || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("identityruntime: invalid assignment %q", assignment)
		}
		for _, value := range strings.Split(values, ",") {
			if normalized := strings.TrimSpace(value); normalized != "" {
				result[strings.TrimSpace(name)] = append(result[strings.TrimSpace(name)], normalized)
			}
		}
	}
	return result, nil
}

func ParseOwners(raw string) (map[string]string, error) {
	assignments, err := ParseAssignments(raw)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(assignments))
	for resource, merchants := range assignments {
		if len(merchants) != 1 {
			return nil, fmt.Errorf("identityruntime: resource %q must have exactly one owner", resource)
		}
		owners[resource] = merchants[0]
	}
	return owners, nil
}

func ParseUint64(raw string, fallback uint64) (uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("identityruntime: parse uint64: %w", err)
	}
	return value, nil
}

func ParseDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("identityruntime: duration must be positive: %q", raw)
	}
	return value, nil
}
