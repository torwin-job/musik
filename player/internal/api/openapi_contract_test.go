package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	var document struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIJSON, &document); err != nil {
		t.Fatalf("parse embedded OpenAPI: %v", err)
	}

	registered := make(map[string]struct{}, len(apiRoutes))
	for _, route := range apiRoutes {
		registered[route.method+" "+route.path] = struct{}{}
	}
	documented := make(map[string]struct{}, len(apiRoutes))
	for path, pathItem := range document.Paths {
		for method := range pathItem {
			method = strings.ToUpper(method)
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
				documented[method+" "+path] = struct{}{}
			}
		}
	}

	for route := range registered {
		if _, ok := documented[route]; !ok {
			t.Errorf("registered route missing from OpenAPI: %s", route)
		}
	}
	for route := range documented {
		if _, ok := registered[route]; !ok {
			t.Errorf("OpenAPI route is not registered: %s", route)
		}
	}

	var eventSchema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(document.Components.Schemas["PlaybackEvent"], &eventSchema); err != nil {
		t.Fatalf("parse PlaybackEvent schema: %v", err)
	}
	required := map[string]bool{}
	for _, name := range eventSchema.Required {
		required[name] = true
	}
	for _, name := range []string{"type", "track_id", "session_id"} {
		if !required[name] {
			t.Errorf("PlaybackEvent does not require %s", name)
		}
		if eventSchema.Properties[name] == nil {
			t.Errorf("PlaybackEvent does not define %s", name)
		}
	}
	if required["event_id"] {
		t.Error("PlaybackEvent must keep event_id optional for legacy mobile requests")
	}
	if eventSchema.Properties["event_id"] == nil {
		t.Error("PlaybackEvent does not define optional event_id")
	}
	if eventSchema.Properties["impression_id"] == nil {
		t.Error("PlaybackEvent does not define impression_id")
	}
	if eventSchema.Properties["client_id"] == nil || eventSchema.Properties["device_id"] == nil {
		t.Error("PlaybackEvent does not define client/device identity")
	}

	for _, name := range []string{"MetricSlice", "BaselineStatus", "RecommendationMetrics"} {
		if document.Components.Schemas[name] == nil {
			t.Errorf("missing %s schema", name)
		}
	}
	var baseline struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(document.Components.Schemas["BaselineStatus"], &baseline); err != nil {
		t.Fatalf("parse BaselineStatus: %v", err)
	}
	for _, name := range []string{"ready", "minimum_days", "minimum_impressions", "eligible_algorithmic_impressions"} {
		found := false
		for _, required := range baseline.Required {
			if required == name {
				found = true
			}
		}
		if !found {
			t.Errorf("BaselineStatus does not require %s", name)
		}
	}

	var eventsOperation struct {
		RequestBody struct {
			Content map[string]struct {
				Schema map[string]string `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(document.Paths["/api/events"]["post"], &eventsOperation); err != nil {
		t.Fatalf("parse /api/events operation: %v", err)
	}
	ref := eventsOperation.RequestBody.Content["application/json"].Schema["$ref"]
	if ref != "#/components/schemas/PlaybackEvent" {
		t.Fatalf("/api/events schema ref = %q", ref)
	}

	for _, name := range []string{"Plugin", "PluginCatalog", "PluginImportRequest", "PluginJob", "PluginImportAccepted", "PluginJobList"} {
		if document.Components.Schemas[name] == nil {
			t.Errorf("missing %s schema", name)
		}
	}
	var pluginJob struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(document.Components.Schemas["PluginJob"], &pluginJob); err != nil {
		t.Fatalf("parse PluginJob schema: %v", err)
	}
	for _, name := range []string{"id", "plugin_id", "action", "status", "error", "error_code", "progress", "result", "downstream_job_id", "created_at", "updated_at"} {
		found := false
		for _, required := range pluginJob.Required {
			if required == name {
				found = true
			}
		}
		if !found {
			t.Errorf("PluginJob does not require %s", name)
		}
	}
	for _, hidden := range []string{"url", "input", "input_json", "executable"} {
		if pluginJob.Properties[hidden] != nil {
			t.Errorf("PluginJob exposes %s", hidden)
		}
	}
	var importOperation struct {
		RequestBody struct {
			Content map[string]struct {
				Schema map[string]string `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(document.Paths["/api/plugins/{id}/import"]["post"], &importOperation); err != nil {
		t.Fatalf("parse plugin import operation: %v", err)
	}
	if got := importOperation.RequestBody.Content["application/json"].Schema["$ref"]; got != "#/components/schemas/PluginImportRequest" {
		t.Fatalf("plugin import schema ref = %q", got)
	}
	var catalogOperation struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema map[string]string `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(document.Paths["/api/plugins"]["get"], &catalogOperation); err != nil {
		t.Fatalf("parse plugin catalog operation: %v", err)
	}
	if got := catalogOperation.Responses["200"].Content["application/json"].Schema["$ref"]; got != "#/components/schemas/PluginCatalog" {
		t.Fatalf("plugin catalog schema ref = %q", got)
	}
}
