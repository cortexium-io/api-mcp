package apimcp

import (
	"regexp"
	"strings"
)

func ShouldExposeOperation(operation OperationDescriptor, filter ToolFilterConfig) bool {
	candidates := FilterCandidates(operation)
	if matchesAny(filter.Exclude, candidates) {
		return false
	}
	if len(filter.Include) > 0 {
		return matchesAny(filter.Include, candidates)
	}
	if filter.DefaultPolicy == "deny" {
		return false
	}
	return true
}

func FilterCandidates(operation OperationDescriptor) []string {
	candidates := []string{
		operation.ToolName,
		operation.OperationID,
		strings.ToUpper(operation.Method) + " " + operation.Path,
	}
	for _, tag := range operation.Tags {
		candidates = append(candidates, "tag:"+tag)
	}
	return compactStrings(candidates)
}

func matchesAny(patterns []string, candidates []string) bool {
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if MatchesPattern(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

func MatchesPattern(pattern, candidate string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, candidate)
	}

	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	re := regexp.MustCompile(`(?i)^` + strings.Join(parts, ".*") + `$`)
	return re.MatchString(candidate)
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
