package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file pins down the **capability boundary** of generic webhook templates.
//
// This is the only place in the package where "user-supplied strings are evaluated as code", so we must be explicit
// about what it can and cannot do, and fix those properties with tests; otherwise someone might casually add a
// method to the template context or a readFile to the FuncMap, silently widening the capability surface while the
// diff looks like just a harmless little function.

// TestTemplateContextHasNoMethods is the most important one.
//
// text/template calls exported methods ({{.Foo}} can read a field or call a method). So if the template context can
// reach **any** type with exported methods, those methods are exposed to template authors.
// This feature's context is deliberately pure data (exported fields only, zero methods).
//
// If this fails: someone added a method to webhookTemplateData / webhookItem.
// Before allowing it, think through whether templates could use that method to read things that shouldn't be exposed.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s exposes %d methods (%s): text/template can call them, "+
				"which opens those methods' capabilities to template authors", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal pins down the set of functions exposed to templates.
//
// Every extra function in the FuncMap is an extra capability. Currently there are only json / jsons, which serialize
// values to JSON fragments: they can't read files, make requests or execute commands.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("template function set changed: got %v, want %v. Before adding a function make sure it doesn't widen the capability surface "+
			"(no reading/writing files, no network requests, no command execution)", got, want)
	}
}

// TestTemplateCannotReachUnknownData covers out-of-bounds access in templates:
// accessing something nonexistent must fail rather than echo something back, and the error must not carry internal data.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("accessing a nonexistent field should error")
	}
	// The error must not contain real content from the template context (finding title/summary).
	for _, leak := range []string{"SQL injection", "parameter id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("template error leaked message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently: a broken template is a config error that retrying won't fix.
// If it were judged retryable, one bad template would make every delivery waste three rounds of backoff.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("template syntax errors should be caught at save time")
	}
	// Even when delivering directly bypassing validation, it must be a permanent failure rather than retried repeatedly.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("a bad template should be a permanent failure, got %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON covers the constraint "template output must be valid JSON".
// It also blocks uses like "generate plain text with the template to trigger some other protocol".
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// A valid template passes.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("a valid template should pass validation: %v", err)
	}
	// Non-JSON output must be rejected (rather than sent as-is).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("rendering non-JSON should be a permanent failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("error message should say it is a JSON problem, got %v", err)
	}
}
