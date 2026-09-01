package apimcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

var testOperation = OperationDescriptor{
	ToolName:    "get_pet_by_id",
	Method:      "get",
	Path:        "/pet/{petId}",
	ServerURL:   "https://api.example.com/v1",
	OperationID: "getPetById",
	Title:       "Get pet",
	Description: "Get pet",
	Tags:        []string{"pets"},
}

var testHTTPConfig = Config{
	Name:             "test",
	Version:          "0.1.0",
	Spec:             "unused",
	Auth:             ResolvedAuthConfig{Type: "bearer", Token: "secret-token"},
	Headers:          map[string]string{"User-Agent": "api-mcp-test"},
	Tools:            ToolFilterConfig{},
	MaxResponseChars: 20_000,
}

func TestBuildHTTPRequestAppliesPathQueryHeadersCookiesAndAuth(t *testing.T) {
	request, err := BuildHTTPRequest(context.Background(), testOperation, ToolInput{
		Path:    map[string]any{"petId": 123},
		Query:   map[string]any{"include": true, "tag": []any{"a", "b"}},
		Headers: map[string]any{"X-Request-ID": "request-1"},
		Cookies: map[string]any{"session": "abc 123"},
	}, testHTTPConfig)
	if err != nil {
		t.Fatal(err)
	}

	parsedURL := request.URL
	if got, want := parsedURL.Scheme+"://"+parsedURL.Host, "https://api.example.com"; got != want {
		t.Fatalf("origin = %q, want %q", got, want)
	}
	if got, want := parsedURL.Path, "/v1/pet/123"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got, want := parsedURL.Query().Get("include"), "true"; got != want {
		t.Fatalf("include = %q, want %q", got, want)
	}
	if got, want := parsedURL.Query()["tag"], []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tag = %#v, want %#v", got, want)
	}
	if got, want := request.Header.Get("authorization"), "Bearer secret-token"; got != want {
		t.Fatalf("authorization = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("x-request-id"), "request-1"; got != want {
		t.Fatalf("x-request-id = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("cookie"), "session=abc%20123"; got != want {
		t.Fatalf("cookie = %q, want %q", got, want)
	}
}

func TestBuildHTTPRequestSerializesJSONBodiesAndAPIKeyQueryAuth(t *testing.T) {
	operation := testOperation
	operation.Method = "post"
	operation.RequestBody = &RequestBodyDescriptor{
		Required:     true,
		ContentTypes: []string{"application/json"},
	}

	request, err := BuildHTTPRequest(context.Background(), operation, ToolInput{
		Path: map[string]any{"petId": 123},
		Body: map[string]any{"name": "Fluffy"},
	}, Config{
		Name:             "test",
		Version:          "0.1.0",
		Spec:             "unused",
		Auth:             ResolvedAuthConfig{Type: "apiKey", In: "query", Name: "api_key", Value: "secret"},
		Headers:          map[string]string{},
		Tools:            ToolFilterConfig{},
		MaxResponseChars: 20_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	parsedURL := request.URL
	if got, want := parsedURL.Query().Get("api_key"), "secret"; got != want {
		t.Fatalf("api_key = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("content-type"), "application/json"; got != want {
		t.Fatalf("content-type = %q, want %q", got, want)
	}

	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["name"], "Fluffy"; got != want {
		t.Fatalf("body.name = %q, want %q", got, want)
	}
}

func TestBuildHTTPRequestAppliesAPIKeyCookieAuth(t *testing.T) {
	request, err := BuildHTTPRequest(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, Config{
		Name:             "test",
		Version:          "0.1.0",
		Spec:             "unused",
		Auth:             ResolvedAuthConfig{Type: "apiKey", In: "cookies", Name: "api_key", Value: "secret"},
		Headers:          map[string]string{},
		Tools:            ToolFilterConfig{},
		MaxResponseChars: 20_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := request.Header.Get("cookie"), "api_key=secret"; got != want {
		t.Fatalf("cookie = %q, want %q", got, want)
	}
}

func TestExecuteOperationParsesJSONResponse(t *testing.T) {
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioNopCloser(`{"ok":true}`),
		}, nil
	})

	response, err := ExecuteOperation(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, testHTTPConfig, client)
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Status != 200 {
		t.Fatalf("unexpected response: %+v", response)
	}
	body, ok := response.Body.(map[string]any)
	if !ok || body["ok"] != true {
		t.Fatalf("unexpected body: %#v", response.Body)
	}
}

func TestExecuteOperationDiscardsTruncatedJSONAndReturnsStructuredRetry(t *testing.T) {
	cfg := testHTTPConfig
	cfg.MaxResponseChars = 32
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioNopCloser(`{"records":[{"id":"one"},{"id":"two"},{"id":"three"}]}`),
		}, nil
	})

	response, err := ExecuteOperation(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK {
		t.Fatal("truncated response must not be reported as complete success")
	}
	if !response.Truncated || response.Truncation == nil {
		t.Fatalf("missing structured truncation metadata: %+v", response)
	}
	if !response.Truncation.PartialBodyDiscarded {
		t.Fatalf("partial body should be discarded: %+v", response.Truncation)
	}
	if response.Truncation.Code != "response_body_truncated" {
		t.Fatalf("truncation code = %q", response.Truncation.Code)
	}
	if response.Truncation.LimitBytes != cfg.MaxResponseChars || response.Truncation.ReceivedBytesAtLeast != cfg.MaxResponseChars+1 {
		t.Fatalf("unexpected truncation byte counts: %+v", response.Truncation)
	}
	body, ok := response.Body.(map[string]any)
	if !ok || body["error"] != "response_body_truncated" {
		t.Fatalf("unexpected truncation body: %#v", response.Body)
	}
	if strings.Contains(response.Truncation.RetryAdvice, "three") || strings.Contains(response.Truncation.RetryAdvice, "records") {
		t.Fatalf("retry advice leaked partial body: %q", response.Truncation.RetryAdvice)
	}
}

func TestExecuteOperationExtractsProvenPaginationMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{"paging":{"page":2,"pageCount":2,"pageSize":50,"total":75,"nextUrl":""}},"records":[]}`))
	}))
	defer server.Close()

	operation := OperationDescriptor{Method: "get", Path: "/records", ServerURL: server.URL}
	response, err := ExecuteOperation(context.Background(), operation, ToolInput{}, Config{Auth: ResolvedAuthConfig{Type: "none"}, MaxResponseChars: 20_000}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if response.Pagination == nil || response.Pagination.Source != "meta.paging" || !response.Pagination.FinalPage {
		t.Fatalf("pagination = %+v", response.Pagination)
	}
	if response.Pagination.Page != 2 || response.Pagination.PageCount != 2 || response.Pagination.Total != 75 {
		t.Fatalf("pagination = %+v", response.Pagination)
	}
}

func TestExecuteOperationDoesNotInferPaginationWithoutPageCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{"paging":{"page":1}},"records":[]}`))
	}))
	defer server.Close()

	operation := OperationDescriptor{Method: "get", Path: "/records", ServerURL: server.URL}
	response, err := ExecuteOperation(context.Background(), operation, ToolInput{}, Config{Auth: ResolvedAuthConfig{Type: "none"}, MaxResponseChars: 20_000}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if response.Pagination != nil {
		t.Fatalf("pagination was inferred without complete metadata: %+v", response.Pagination)
	}
}

func TestExecuteOperationRedactsAuthFromErrors(t *testing.T) {
	cfg := testHTTPConfig
	cfg.Auth = ResolvedAuthConfig{Type: "apiKey", In: "query", Name: "api_key", Value: "top-secret-token"}
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New("request failed for top-secret-token")}
	})

	_, err := ExecuteOperation(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, cfg, client)
	if err == nil {
		t.Fatal("expected request error")
	}
	if strings.Contains(err.Error(), "top-secret-token") {
		t.Fatalf("error leaked credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error should mark redaction: %q", err.Error())
	}
}

func TestExecuteOperationRedactsAuthFromResponseHeadersAndBody(t *testing.T) {
	cfg := testHTTPConfig
	cfg.Auth = ResolvedAuthConfig{Type: "bearer", Token: "top-secret-token"}
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Echo":       []string{"Bearer top-secret-token"},
			},
			Body: ioNopCloser(`{"token":"top-secret-token","nested":["Bearer top-secret-token"]}`),
		}, nil
	})

	response, err := ExecuteOperation(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret-token") {
		t.Fatalf("response leaked credential: %s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("response should mark redactions: %s", data)
	}
}

func TestExecuteOperationRedactsAuthFromPaginationURL(t *testing.T) {
	cfg := testHTTPConfig
	cfg.Auth = ResolvedAuthConfig{Type: "apiKey", In: "query", Name: "api_key", Value: "top-secret-token"}
	client := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioNopCloser(`{"meta":{"paging":{"page":1,"pageCount":2,"nextUrl":"https://api.example.test/records?page=2&api_key=top-secret-token"}},"records":[]}`),
		}, nil
	})

	response, err := ExecuteOperation(context.Background(), testOperation, ToolInput{Path: map[string]any{"petId": 123}}, cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	if response.Pagination == nil || strings.Contains(response.Pagination.NextURL, "top-secret-token") || !strings.Contains(response.Pagination.NextURL, "[REDACTED]") {
		t.Fatalf("pagination URL was not redacted: %+v", response.Pagination)
	}
}

func TestExecuteOperationRedactsSecretAtomsFromCompositeAuthHeaders(t *testing.T) {
	cfg := testHTTPConfig
	cfg.Auth = ResolvedAuthConfig{
		Type:            "headers",
		Headers:         map[string]string{"X-Custom-Auth": "Token top-secret-token; tenant=public"},
		RedactionValues: []string{"top-secret-token"},
	}
	client := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioNopCloser(`{"echo":"top-secret-token"}`),
		}, nil
	})
	response, err := ExecuteOperation(context.Background(), testOperation, ToolInput{Path: map[string]any{"petId": 123}}, cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret-token") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("composite auth secret was not redacted: %s", data)
	}
}

func TestExecuteOperationRedactsAuthFromResponseReadErrors(t *testing.T) {
	cfg := testHTTPConfig
	cfg.Auth = ResolvedAuthConfig{Type: "bearer", Token: "top-secret-token"}
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       errorReadCloser{err: errors.New("read failed for top-secret-token")},
		}, nil
	})

	_, err := ExecuteOperation(context.Background(), testOperation, ToolInput{
		Path: map[string]any{"petId": 123},
	}, cfg, client)
	if err == nil {
		t.Fatal("expected response read error")
	}
	if strings.Contains(err.Error(), "top-secret-token") {
		t.Fatalf("error leaked credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error should mark redaction: %q", err.Error())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func ioNopCloser(value string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(value))
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (errorReadCloser) Close() error {
	return nil
}
