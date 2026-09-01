package apimcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type ToolInput struct {
	Profile     string         `json:"profile,omitempty"`
	Path        map[string]any `json:"path,omitempty"`
	Query       map[string]any `json:"query,omitempty"`
	Headers     map[string]any `json:"headers,omitempty"`
	Cookies     map[string]any `json:"cookies,omitempty"`
	ContentType string         `json:"contentType,omitempty"`
	Body        any            `json:"body,omitempty"`
}

type APIResponse struct {
	Profile    *APIProfile       `json:"profile,omitempty"`
	OK         bool              `json:"ok"`
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	Headers    map[string]string `json:"headers"`
	Body       any               `json:"body"`
	Truncated  bool              `json:"truncated"`
	Truncation *TruncationInfo   `json:"truncation,omitempty"`
	Pagination *PaginationInfo   `json:"pagination,omitempty"`
}

type TruncationInfo struct {
	Code                 string `json:"code"`
	LimitBytes           int    `json:"limitBytes"`
	ReceivedBytesAtLeast int    `json:"receivedBytesAtLeast"`
	PartialBodyDiscarded bool   `json:"partialBodyDiscarded"`
	RetryAdvice          string `json:"retryAdvice"`
}

type PaginationInfo struct {
	Source    string `json:"source"`
	Page      int    `json:"page"`
	PageCount int    `json:"pageCount"`
	PageSize  int    `json:"pageSize,omitempty"`
	Total     int    `json:"total,omitempty"`
	NextURL   string `json:"nextUrl,omitempty"`
	FinalPage bool   `json:"finalPage"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func ExecuteOperation(ctx context.Context, operation OperationDescriptor, input ToolInput, cfg Config, client HTTPDoer) (APIResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := BuildHTTPRequest(ctx, operation, input, cfg)
	if err != nil {
		return APIResponse{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return APIResponse{}, errors.New(redactAuthText(err.Error(), cfg.Auth))
	}
	defer response.Body.Close()

	maxChars := cfg.MaxResponseChars
	if maxChars <= 0 {
		maxChars = 20_000
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(maxChars)+1))
	if err != nil {
		return APIResponse{}, errors.New(redactAuthText(err.Error(), cfg.Auth))
	}
	receivedBytes := len(data)
	truncated := receivedBytes > maxChars
	if truncated {
		data = data[:maxChars]
	}

	httpOK := response.StatusCode >= 200 && response.StatusCode < 300
	result := APIResponse{
		OK:         httpOK && !truncated,
		Status:     response.StatusCode,
		StatusText: response.Status,
		Headers:    flattenHeaders(response.Header),
		Truncated:  truncated,
	}
	if truncated {
		result.Truncation = &TruncationInfo{
			Code:                 "response_body_truncated",
			LimitBytes:           maxChars,
			ReceivedBytesAtLeast: receivedBytes,
			PartialBodyDiscarded: true,
			RetryAdvice:          "Retry with a smaller pageSize, the next explicit page, or narrower filters. Do not treat this response as a complete collection.",
		}
		result.Body = map[string]any{
			"error":   "response_body_truncated",
			"message": "The upstream response exceeded the configured MCP response limit. The partial body was discarded.",
		}
	} else {
		result.Body = parseResponseBody(data, response.Header.Get("content-type"))
		result.Pagination = extractPagination(result.Body)
	}
	redactAuthFromResponse(&result, cfg.Auth)
	return result, nil
}

func extractPagination(body any) *PaginationInfo {
	root, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	candidates := []struct {
		path  string
		value any
	}{
		{path: "meta.paging", value: nestedMapValue(root, "meta", "paging")},
		{path: "meta.pagination", value: nestedMapValue(root, "meta", "pagination")},
		{path: "paging", value: root["paging"]},
		{path: "pagination", value: root["pagination"]},
	}
	for _, candidate := range candidates {
		paging, ok := candidate.value.(map[string]any)
		if !ok {
			continue
		}
		page, pageOK := integerValue(paging["page"])
		pageCount, countOK := integerValue(paging["pageCount"])
		if !pageOK || !countOK || page < 1 || pageCount < 1 {
			continue
		}
		pageSize, _ := integerValue(paging["pageSize"])
		total, _ := integerValue(paging["total"])
		nextURL, _ := paging["nextUrl"].(string)
		return &PaginationInfo{
			Source:    candidate.path,
			Page:      page,
			PageCount: pageCount,
			PageSize:  pageSize,
			Total:     total,
			NextURL:   nextURL,
			FinalPage: page >= pageCount,
		}
	}
	return nil
}

func nestedMapValue(root map[string]any, first, second string) any {
	nested, ok := root[first].(map[string]any)
	if !ok {
		return nil
	}
	return nested[second]
}

func integerValue(value any) (int, bool) {
	switch value := value.(type) {
	case float64:
		integer := int(value)
		return integer, value == float64(integer)
	case int:
		return value, true
	case json.Number:
		integer, err := value.Int64()
		return int(integer), err == nil
	default:
		return 0, false
	}
}

func BuildHTTPRequest(ctx context.Context, operation OperationDescriptor, input ToolInput, cfg Config) (*http.Request, error) {
	if cfg.Auth.CredentialMissing {
		return nil, errors.New("API credential is not configured")
	}
	if operation.ServerURL == "" {
		return nil, fmt.Errorf("no server URL configured for %s %s; set config.serverUrl or add OpenAPI servers", strings.ToUpper(operation.Method), operation.Path)
	}

	rawURL, err := applyPathParams(operation.ServerURL, operation.Path, input.Path)
	if err != nil {
		return nil, err
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	appendQueryParams(parsedURL, input.Query)

	headers := make(http.Header)
	for name, value := range cfg.Headers {
		headers.Set(name, value)
	}
	appendHeaderParams(headers, input.Headers)
	appendCookies(headers, input.Cookies)

	var body io.Reader
	if input.Body != nil {
		contentType := chooseContentType(operation, input.ContentType)
		if contentType != "" && headers.Get("content-type") == "" {
			headers.Set("content-type", contentType)
		}
		serializedBody, err := serializeBody(input.Body, contentType)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(serializedBody)
	}

	applyAuth(parsedURL, headers, cfg.Auth)

	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(operation.Method), parsedURL.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header = headers
	return request, nil
}

var pathParamPattern = regexp.MustCompile(`\{([^}]+)\}`)

func applyPathParams(serverURL, operationPath string, pathParams map[string]any) (string, error) {
	base := strings.TrimRight(serverURL, "/")
	path := operationPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var resultErr error
	result := pathParamPattern.ReplaceAllStringFunc(base+path, func(match string) string {
		if resultErr != nil {
			return ""
		}
		name := strings.Trim(match, "{}")
		name = strings.TrimLeft(name, "+.#;?&")
		value, ok := pathParams[name]
		if !ok || value == nil {
			resultErr = fmt.Errorf("missing required path parameter: %s", name)
			return ""
		}
		return url.PathEscape(fmt.Sprint(value))
	})
	if resultErr != nil {
		return "", resultErr
	}
	return result, nil
}

func appendQueryParams(parsedURL *url.URL, query map[string]any) {
	values := parsedURL.Query()
	for name, value := range query {
		appendURLValue(values, name, value)
	}
	parsedURL.RawQuery = values.Encode()
}

func appendURLValue(values url.Values, name string, value any) {
	if value == nil {
		return
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			appendURLValue(values, name, item)
		}
		return
	}
	values.Add(name, fmt.Sprint(value))
}

func appendHeaderParams(headers http.Header, headerParams map[string]any) {
	for name, value := range headerParams {
		if value != nil {
			headers.Set(name, fmt.Sprint(value))
		}
	}
}

func appendCookies(headers http.Header, cookies map[string]any) {
	if len(cookies) == 0 {
		return
	}
	parts := []string{}
	if existing := headers.Get("cookie"); existing != "" {
		parts = append(parts, existing)
	}
	for name, value := range cookies {
		if value == nil {
			continue
		}
		parts = append(parts, url.PathEscape(name)+"="+url.PathEscape(fmt.Sprint(value)))
	}
	if len(parts) > 0 {
		headers.Set("cookie", strings.Join(parts, "; "))
	}
}

func applyAuth(parsedURL *url.URL, headers http.Header, auth ResolvedAuthConfig) {
	switch auth.Type {
	case "", "none":
		return
	case "bearer":
		headers.Set("authorization", "Bearer "+auth.Token)
	case "basic":
		token := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
		headers.Set("authorization", "Basic "+token)
	case "apiKey":
		switch auth.In {
		case "headers":
			headers.Set(auth.Name, auth.Value)
		case "query":
			values := parsedURL.Query()
			values.Set(auth.Name, auth.Value)
			parsedURL.RawQuery = values.Encode()
		case "cookies":
			appendCookies(headers, map[string]any{auth.Name: auth.Value})
		}
	case "headers":
		for name, value := range auth.Headers {
			headers.Set(name, value)
		}
	}
}

func redactAuthFromResponse(response *APIResponse, auth ResolvedAuthConfig) {
	secrets := authRedactionValues(auth)
	response.StatusText = redactText(response.StatusText, secrets)
	for name, value := range response.Headers {
		response.Headers[name] = redactText(value, secrets)
	}
	if response.Pagination != nil {
		response.Pagination.NextURL = redactText(response.Pagination.NextURL, secrets)
	}
	response.Body = redactValue(response.Body, secrets)
}

func redactAuthText(value string, auth ResolvedAuthConfig) string {
	return redactText(value, authRedactionValues(auth))
}

func authRedactionValues(auth ResolvedAuthConfig) []string {
	var values []string
	add := func(value string) {
		if value == "" {
			return
		}
		for _, existing := range values {
			if existing == value {
				return
			}
		}
		values = append(values, value)
	}

	switch auth.Type {
	case "bearer":
		add(auth.Token)
		add("Bearer " + auth.Token)
	case "basic":
		add(auth.Password)
		add(auth.Username + ":" + auth.Password)
		add("Basic " + base64.StdEncoding.EncodeToString([]byte(auth.Username+":"+auth.Password)))
	case "apiKey":
		add(auth.Value)
	case "headers":
		for _, value := range auth.RedactionValues {
			add(value)
		}
		for _, value := range auth.Headers {
			add(value)
		}
	}
	return values
}

func redactValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case string:
		return redactText(typed, secrets)
	case []any:
		for i, item := range typed {
			typed[i] = redactValue(item, secrets)
		}
		return typed
	case map[string]any:
		for name, item := range typed {
			typed[name] = redactValue(item, secrets)
		}
		return typed
	default:
		return value
	}
}

func redactText(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func chooseContentType(operation OperationDescriptor, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if operation.RequestBody == nil {
		return ""
	}
	for _, contentType := range operation.RequestBody.ContentTypes {
		if isJSONContentType(contentType) {
			return contentType
		}
	}
	if len(operation.RequestBody.ContentTypes) > 0 {
		return operation.RequestBody.ContentTypes[0]
	}
	return ""
}

func serializeBody(body any, contentType string) ([]byte, error) {
	if contentType == "application/x-www-form-urlencoded" {
		values := url.Values{}
		bodyMap, ok := body.(map[string]any)
		if !ok {
			return nil, errors.New("application/x-www-form-urlencoded body must be an object")
		}
		for name, value := range bodyMap {
			appendURLValue(values, name, value)
		}
		return []byte(values.Encode()), nil
	}
	if isJSONContentType(contentType) {
		return json.Marshal(body)
	}
	if text, ok := body.(string); ok {
		return []byte(text), nil
	}
	return json.Marshal(body)
}

func parseResponseBody(data []byte, contentType string) any {
	if len(data) == 0 {
		return nil
	}
	if isJSONContentType(contentType) {
		var value any
		if err := json.Unmarshal(data, &value); err == nil {
			return value
		}
	}
	return string(data)
}

func flattenHeaders(headers http.Header) map[string]string {
	result := map[string]string{}
	for name, values := range headers {
		result[name] = strings.Join(values, ", ")
	}
	return result
}

func isJSONContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = contentType
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
