package apimcp

import (
	"fmt"
	"mime"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func validateToolInput(operation OperationDescriptor, input ToolInput, bodyPresent bool) error {
	if operation.RequestBody == nil {
		if bodyPresent || input.ContentType != "" {
			return fmt.Errorf("%s %s does not declare a request body", strings.ToUpper(operation.Method), operation.Path)
		}
	} else {
		if operation.RequestBody.Required && !bodyPresent {
			return fmt.Errorf("missing required request body for %s %s", strings.ToUpper(operation.Method), operation.Path)
		}
		if input.ContentType != "" && !containsContentType(operation.RequestBody.ContentTypes, input.ContentType) {
			return fmt.Errorf("unsupported contentType %q for %s %s", input.ContentType, strings.ToUpper(operation.Method), operation.Path)
		}
		if bodyPresent {
			contentType := chooseContentType(operation, input.ContentType)
			if schema := requestBodySchema(operation.RequestBody, contentType); schema != nil && schema.Value != nil {
				if err := schema.Value.VisitJSON(input.Body, openapi3.EnableJSONSchema2020(), openapi3.VisitAsRequest()); err != nil {
					return fmt.Errorf("invalid request body: %w", err)
				}
			}
		}
	}

	groups := map[string]map[string]any{
		"path":   input.Path,
		"query":  input.Query,
		"header": input.Headers,
		"cookie": input.Cookies,
	}
	for location, values := range groups {
		declarations := parametersForLocation(operation.Parameters, location)
		for name := range values {
			if findParameter(declarations, location, name) == nil {
				return fmt.Errorf("undeclared %s parameter %q for %s %s", location, name, strings.ToUpper(operation.Method), operation.Path)
			}
		}
		for i := range declarations {
			parameter := &declarations[i]
			value, found := parameterValue(values, location, parameter.Name)
			if parameter.Required && (!found || value == nil) {
				return fmt.Errorf("missing required %s parameter: %s", location, parameter.Name)
			}
			if found && parameter.Schema != nil && parameter.Schema.Value != nil {
				if err := parameter.Schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020(), openapi3.VisitAsRequest()); err != nil {
					return fmt.Errorf("invalid %s parameter %q: %w", location, parameter.Name, err)
				}
			}
		}
	}
	return nil
}

func parametersForLocation(parameters []ParameterDescriptor, location string) []ParameterDescriptor {
	result := make([]ParameterDescriptor, 0)
	for _, parameter := range parameters {
		if parameter.In == location {
			result = append(result, parameter)
		}
	}
	return result
}

func findParameter(parameters []ParameterDescriptor, location, name string) *ParameterDescriptor {
	for i := range parameters {
		if parameters[i].Name == name || (location == "header" && strings.EqualFold(parameters[i].Name, name)) {
			return &parameters[i]
		}
	}
	return nil
}

func parameterValue(values map[string]any, location, name string) (any, bool) {
	value, ok := values[name]
	if ok || location != "header" {
		return value, ok
	}
	for key, candidate := range values {
		if strings.EqualFold(key, name) {
			return candidate, true
		}
	}
	return nil, false
}

func containsContentType(allowed []string, candidate string) bool {
	for _, value := range allowed {
		if sameMediaType(value, candidate) {
			return true
		}
	}
	return false
}

func sameMediaType(left, right string) bool {
	leftType, _, leftErr := mime.ParseMediaType(left)
	rightType, _, rightErr := mime.ParseMediaType(right)
	if leftErr != nil || rightErr != nil {
		return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
	}
	return strings.EqualFold(leftType, rightType)
}

func requestBodySchema(body *RequestBodyDescriptor, contentType string) *openapi3.SchemaRef {
	if body == nil {
		return nil
	}
	for declared, schema := range body.Schemas {
		if sameMediaType(declared, contentType) {
			return schema
		}
	}
	return nil
}
