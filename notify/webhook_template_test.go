package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file enforces the capabilities and security boundaries of generic Webhook templates.
//
// This is the only place in the entire package where user-supplied strings are evaluated as code.
// Therefore, we strictly delineate what it can and cannot do, locking down these properties with tests.
// Otherwise, someone could add a method to the template context or add readFile to the FuncMap,
// silently expanding the capability surface.

// TestTemplateContextHasNoMethods is the most important constraint.
//
// text/template can invoke exported methods ({{.Foo}} accesses fields and calls methods).
// Therefore, if the template context has access to ANY type with exported methods, it exposes
// those capabilities to template authors. The context here is deliberately pure data (exported fields only, zero methods).
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s exposed %d methods (%s): text/template could call them, "+
				"effectively granting those capabilities to template authors", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal locks down the set of functions exposed to templates.
//
// Every function in FuncMap represents an expanded capability. Currently only json / jsons exist,
// which serialize values into JSON fragments — no file reads, no network requests, no command executions.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template function set changed: got %v, want %v. Before adding functions, verify "+
			"it does not expand the attack surface (no file I/O, network, or command execution)", got, want)
	}
}

// TestTemplateCannotReachUnknownData covers out-of-bounds access in templates:
// Accessing non-existent fields must fail rather than echo anything; errors must not leak internal data.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("accessing nonexistent fields should error")
	}
	// Template errors must not leak message content.
	for _, leak := range []string{"SQL Injection", "parameter id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("template error leaked message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently: malformed templates are configuration errors that retry cannot heal.
// If classified as retryable, a broken template would waste three rounds of exponential backoff on every dispatch.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("template syntax errors should be caught during validation")
	}
	// Even if validation is bypassed, sending must yield permanent failure rather than repeated retries.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("broken template should fail permanently, got %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON covers the constraint that template render results must be valid JSON.
// It also blocks misuse such as generating plain text to trigger other protocols.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// Valid template succeeds.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("valid template should pass validation: %v", err)
	}
	// Rendering non-JSON must be rejected (not dispatched as-is).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("rendering non-JSON should fail permanently, got %v", err)
	}
	if !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("error message should indicate JSON issue, got %v", err)
	}
}
