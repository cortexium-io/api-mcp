package apimcp

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

var httpMethods = []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}

type ParameterDescriptor struct {
	Name        string
	In          string
	Required    bool
	Description string
	Schema      *openapi3.SchemaRef
}

type RequestBodyDescriptor struct {
	Required     bool
	Description  string
	ContentTypes []string
}

type OperationDescriptor struct {
	ToolName    string
	Method      string
	Path        string
	ServerURL   string
	OperationID string
	Title       string
	Description string
	Tags        []string
	Parameters  []ParameterDescriptor
	RequestBody *RequestBodyDescriptor
}

func LoadOperations(ctx context.Context, cfg Config) ([]OperationDescriptor, error) {
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = true

	var doc *openapi3.T
	var err error
	if isHTTPURL(cfg.Spec) {
		location, parseErr := url.Parse(cfg.Spec)
		if parseErr != nil {
			return nil, parseErr
		}
		doc, err = loader.LoadFromURI(location)
	} else {
		doc, err = loader.LoadFromFile(cfg.Spec)
	}
	if err != nil {
		return nil, err
	}

	operations := CollectOperations(doc, cfg)
	filtered := make([]OperationDescriptor, 0, len(operations))
	for _, operation := range operations {
		if ShouldExposeOperation(operation, cfg.Tools) {
			filtered = append(filtered, operation)
		}
	}
	return filtered, nil
}

func CollectOperations(doc *openapi3.T, cfg Config) []OperationDescriptor {
	if doc.Paths == nil {
		return nil
	}

	var operations []OperationDescriptor
	for _, operationPath := range doc.Paths.Keys() {
		pathItem := doc.Paths.Value(operationPath)
		if pathItem == nil {
			continue
		}

		for _, method := range httpMethods {
			operation := pathItem.GetOperation(method)
			if operation == nil {
				continue
			}
			normalizedMethod := strings.ToLower(method)

			title := operation.Summary
			if title == "" {
				title = operation.OperationID
			}
			if title == "" {
				title = method + " " + operationPath
			}

			operations = append(operations, OperationDescriptor{
				Method:      normalizedMethod,
				Path:        operationPath,
				ServerURL:   resolveServerURL(doc, pathItem, operation, cfg),
				OperationID: operation.OperationID,
				Title:       title,
				Description: buildToolDescription(normalizedMethod, operationPath, operation),
				Tags:        append([]string(nil), operation.Tags...),
				Parameters:  collectParameters(pathItem, operation),
				RequestBody: collectRequestBody(operation),
			})
		}
	}

	assignToolNames(operations)
	return operations
}

func BuildToolInputSchema(operation OperationDescriptor) map[string]any {
	properties := map[string]any{}
	required := []string{}
	groups := groupParameters(operation.Parameters)

	for _, location := range []string{"path", "query", "header", "cookie"} {
		parameters := groups[location]
		if len(parameters) == 0 {
			continue
		}
		key := inputKeyForLocation(location)
		schema, hasRequired := parameterObjectSchema(parameters, parameterLocationTitle(location)+" parameters for "+strings.ToUpper(operation.Method)+" "+operation.Path)
		properties[key] = schema
		if hasRequired {
			required = append(required, key)
		}
	}

	if operation.RequestBody != nil {
		description := "Optional request body content type."
		if len(operation.RequestBody.ContentTypes) > 0 {
			description = "Use one of: " + strings.Join(operation.RequestBody.ContentTypes, ", ")
		}
		properties["contentType"] = map[string]any{
			"type":        "string",
			"description": description,
		}
		bodyDescription := operation.RequestBody.Description
		if bodyDescription == "" {
			bodyDescription = "Request body."
		}
		properties["body"] = map[string]any{"description": bodyDescription}
		if operation.RequestBody.Required {
			required = append(required, "body")
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func addProfileToToolInputSchema(schema map[string]any) map[string]any {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		properties = map[string]any{}
		schema["properties"] = properties
	}
	properties["profile"] = map[string]any{
		"type":        "string",
		"description": "Stable API account profile ID. Optional when exactly one profile exists; required when multiple profiles exist. Use list_api_profiles to discover IDs.",
	}
	return schema
}

func assignToolNames(operations []OperationDescriptor) {
	seen := map[string]int{}
	for i := range operations {
		base := toToolName(operations[i].OperationID)
		if base == "" {
			base = toToolName(operations[i].Method + "_" + operations[i].Path)
		}
		seen[base]++
		if seen[base] == 1 {
			operations[i].ToolName = base
		} else {
			operations[i].ToolName = base + "_" + strconv.Itoa(seen[base])
		}
	}
}

func collectParameters(pathItem *openapi3.PathItem, operation *openapi3.Operation) []ParameterDescriptor {
	parameterMap := map[string]ParameterDescriptor{}
	order := []string{}

	add := func(ref *openapi3.ParameterRef) {
		if ref == nil || ref.Value == nil {
			return
		}
		parameter := ref.Value
		if parameter.Name == "" || !isParameterLocation(parameter.In) {
			return
		}
		key := parameter.In + ":" + parameter.Name
		if _, ok := parameterMap[key]; !ok {
			order = append(order, key)
		}
		parameterMap[key] = ParameterDescriptor{
			Name:        parameter.Name,
			In:          parameter.In,
			Required:    parameter.In == "path" || parameter.Required,
			Description: parameter.Description,
			Schema:      parameter.Schema,
		}
	}

	for _, ref := range pathItem.Parameters {
		add(ref)
	}
	for _, ref := range operation.Parameters {
		add(ref)
	}

	result := make([]ParameterDescriptor, 0, len(order))
	for _, key := range order {
		result = append(result, parameterMap[key])
	}
	return result
}

func collectRequestBody(operation *openapi3.Operation) *RequestBodyDescriptor {
	if operation.RequestBody == nil || operation.RequestBody.Value == nil {
		return nil
	}
	body := operation.RequestBody.Value
	contentTypes := make([]string, 0, len(body.Content))
	for contentType := range body.Content {
		contentTypes = append(contentTypes, contentType)
	}
	sort.Strings(contentTypes)
	return &RequestBodyDescriptor{
		Required:     body.Required,
		Description:  body.Description,
		ContentTypes: contentTypes,
	}
}

func buildToolDescription(method, operationPath string, operation *openapi3.Operation) string {
	lines := []string{}
	if operation.Description != "" {
		lines = append(lines, operation.Description)
	} else if operation.Summary != "" {
		lines = append(lines, operation.Summary)
	}
	lines = append(lines, "HTTP: "+strings.ToUpper(method)+" "+operationPath)
	if len(operation.Parameters) > 0 {
		var names []string
		for _, ref := range operation.Parameters {
			if ref != nil && ref.Value != nil {
				names = append(names, ref.Value.In+"."+ref.Value.Name)
			}
		}
		if len(names) > 0 {
			lines = append(lines, "Parameters: "+strings.Join(names, ", "))
		}
	}
	if operation.RequestBody != nil {
		lines = append(lines, "Accepts a request body. Pass it as `body`.")
	}
	if !isReadOnlyMethod(method) {
		lines = append(lines, "This operation can change data. Before calling this tool, ask the user for explicit confirmation and summarize the intended change.")
	}
	return strings.Join(lines, "\n")
}

func isReadOnlyMethod(method string) bool {
	switch strings.ToLower(method) {
	case "get", "head", "options":
		return true
	default:
		return false
	}
}

func resolveServerURL(doc *openapi3.T, pathItem *openapi3.PathItem, operation *openapi3.Operation, cfg Config) string {
	if cfg.ServerURL != "" {
		return cfg.ServerURL
	}
	if operation.Servers != nil && len(*operation.Servers) > 0 {
		return expandServerURL((*operation.Servers)[0])
	}
	if len(pathItem.Servers) > 0 {
		return expandServerURL(pathItem.Servers[0])
	}
	if len(doc.Servers) > 0 {
		return expandServerURL(doc.Servers[0])
	}
	return ""
}

var serverVariablePattern = regexp.MustCompile(`\{([^}]+)\}`)

func expandServerURL(server *openapi3.Server) string {
	if server == nil || server.URL == "" {
		return ""
	}
	return serverVariablePattern.ReplaceAllStringFunc(server.URL, func(match string) string {
		name := strings.Trim(match, "{}")
		if variable, ok := server.Variables[name]; ok && variable != nil {
			return url.PathEscape(variable.Default)
		}
		return ""
	})
}

func groupParameters(parameters []ParameterDescriptor) map[string][]ParameterDescriptor {
	groups := map[string][]ParameterDescriptor{
		"path":   {},
		"query":  {},
		"header": {},
		"cookie": {},
	}
	for _, parameter := range parameters {
		groups[parameter.In] = append(groups[parameter.In], parameter)
	}
	return groups
}

func parameterObjectSchema(parameters []ParameterDescriptor, description string) (map[string]any, bool) {
	properties := map[string]any{}
	required := []string{}

	for _, parameter := range parameters {
		schema := schemaRefToJSONSchema(parameter.Schema)
		if parameter.Description != "" {
			schema["description"] = parameter.Description
		}
		properties[parameter.Name] = schema
		if parameter.Required {
			required = append(required, parameter.Name)
		}
	}

	result := map[string]any{
		"type":        "object",
		"description": description,
		"properties":  properties,
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result, len(required) > 0
}

func schemaRefToJSONSchema(ref *openapi3.SchemaRef) map[string]any {
	if ref == nil || ref.Value == nil {
		return map[string]any{}
	}
	return schemaToJSONSchema(ref.Value)
}

func schemaToJSONSchema(schema *openapi3.Schema) map[string]any {
	result := map[string]any{}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	if schema.Format != "" {
		result["format"] = schema.Format
	}

	types := schemaTypes(schema)
	if len(types) > 0 {
		if len(types) == 1 {
			result["type"] = types[0]
		} else {
			result["type"] = types
		}
	}

	primaryType := firstNonNull(types)
	switch primaryType {
	case "array":
		if schema.Items != nil {
			result["items"] = schemaRefToJSONSchema(schema.Items)
		}
	case "object":
		addObjectSchemaFields(result, schema)
	default:
		if len(schema.Properties) > 0 {
			result["type"] = "object"
			addObjectSchemaFields(result, schema)
		}
	}

	return result
}

func addObjectSchemaFields(result map[string]any, schema *openapi3.Schema) {
	if len(schema.Properties) > 0 {
		properties := map[string]any{}
		names := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			properties[name] = schemaRefToJSONSchema(schema.Properties[name])
		}
		result["properties"] = properties
	}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}
}

func schemaTypes(schema *openapi3.Schema) []string {
	if schema.Type == nil || schema.Type.IsEmpty() {
		if schema.Nullable {
			return []string{"null"}
		}
		return nil
	}
	types := append([]string(nil), schema.Type.Slice()...)
	if schema.Nullable && !slices.Contains(types, "null") {
		types = append(types, "null")
	}
	return types
}

func firstNonNull(types []string) string {
	for _, typ := range types {
		if typ != "null" {
			return typ
		}
	}
	return ""
}

func inputKeyForLocation(location string) string {
	switch location {
	case "header":
		return "headers"
	case "cookie":
		return "cookies"
	default:
		return location
	}
}

func parameterLocationTitle(location string) string {
	switch location {
	case "path":
		return "Path"
	case "query":
		return "Query"
	case "header":
		return "Header"
	case "cookie":
		return "Cookie"
	default:
		return location
	}
}

func isParameterLocation(value string) bool {
	return value == "path" || value == "query" || value == "header" || value == "cookie"
}

var (
	camelBoundaryPattern = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	repeatedUnderscores  = regexp.MustCompile(`_+`)
)

func toToolName(value string) string {
	if value == "" {
		return ""
	}
	result := camelBoundaryPattern.ReplaceAllString(value, "${1}_${2}")
	result = invalidToolNameChars.ReplaceAllString(result, "_")
	result = repeatedUnderscores.ReplaceAllString(result, "_")
	result = strings.ToLower(strings.Trim(result, "_"))
	if len(result) > 64 {
		result = strings.TrimRight(result[:64], "_")
	}
	if result == "" {
		return "operation"
	}
	return result
}
