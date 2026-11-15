package plugins

import (
	"fmt"
	"strings"
	"testing"
)

func TestExampleMessages(t *testing.T) {
	described, err := ParseDescribe(mustObject(t, string(exampleBytes(t, "describe.json"))))
	if err != nil || described.ID != "example" || described.Capabilities[0] != CapabilityImportURL {
		t.Fatalf("describe = %#v, %v", described, err)
	}
	request, err := ParseRequest(mustObject(t, string(exampleBytes(t, "request.json"))))
	if err != nil || request.ID != "job-1" || request.Action != CapabilityImportURL {
		t.Fatalf("request = %#v, %v", request, err)
	}
	if request.Input["dest_dir"] != "imports/example" {
		t.Fatalf("input = %#v", request.Input)
	}
	progress, err := ParseProgress(mustObject(t, string(exampleBytes(t, "progress.json"))))
	if err != nil || progress.Pct == nil || *progress.Pct != 40 || progress.Phase != "download" {
		t.Fatalf("progress = %#v, %v", progress, err)
	}
	result, err := ParseResult(mustObject(t, string(exampleBytes(t, "result.json"))))
	if err != nil || len(result.Paths) != 1 || result.Paths[0] != "imports/example/track.flac" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	message, err := ParseError(mustObject(t, string(exampleBytes(t, "error.json"))))
	if err != nil || message.Code != CodeUnsupportedURL || message.Message != "URL is not supported" {
		t.Fatalf("error = %#v, %v", message, err)
	}
}

func TestProtocolMustBeTheExactString(t *testing.T) {
	raw := `{"type":"request","protocol":PROTOCOL,"id":"job-1","action":"library.import_url","input":{"url":"https://example.invalid","library":"/var/lib/musik"}}`
	for _, protocol := range []string{`1`, `"2"`, `"1.0"`, `"v1"`, `true`, `null`} {
		body := strings.Replace(raw, "PROTOCOL", protocol, 1)
		_, err := ParseRequest(mustObject(t, body))
		if !IsCode(err, CodeProtocolMismatch) {
			t.Fatalf("protocol %s err = %v", protocol, err)
		}
	}
}

func TestRequestKeepsUnknownFields(t *testing.T) {
	request, err := ParseRequest(mustObject(t, string(exampleBytes(t, "request-with-extension.json"))))
	if err != nil {
		t.Fatal(err)
	}
	if request.Extra["trace"] != "abc" || request.Input["label"] != "keep" {
		t.Fatalf("request = %#v", request)
	}
	if request.Input["dest_dir"] != nil {
		t.Fatalf("dest_dir = %#v", request.Input["dest_dir"])
	}
}

func TestSearchExampleAndRequest(t *testing.T) {
	result, err := ParseSearchResult(mustObject(t, string(exampleBytes(t, "search-result.json"))))
	if err != nil || result.ID != "job-1" || len(result.Items) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	item := result.Items[0]
	if item.Title != "Example Track" || item.Artist != "Example Artist" || item.URL != "https://example.invalid/track" || item.Duration == nil || *item.Duration != 248 {
		t.Fatalf("item = %#v", item)
	}
	query := "example track"
	line, err := buildSearchRequest("job-1", query, DefaultSearchLimit)
	if err != nil {
		t.Fatal(err)
	}
	request, err := ParseRequest(mustObject(t, string(line)))
	if err != nil || request.Action != CapabilitySearch || request.Input["query"] != query {
		t.Fatalf("request = %#v, %v", request, err)
	}
	if _, err := ParseSearchResult(mustObject(t, `{"type":"result","protocol":"1","id":"job-1","result":{"items":[{"title":"Example","url":"bad\nurl"}]}}`)); err == nil {
		t.Fatal("control character in url accepted")
	}
	if _, err := buildSearchRequest("job-1", "bad\nquery", 8); err == nil {
		t.Fatal("control character in query accepted")
	}
}

func TestBuildImportRequestRoundTrip(t *testing.T) {
	url := "https://example.invalid/a?b=1&c=2"
	line, err := buildImportRequest("job-1", url, "/var/lib/musik/library", "imports/example")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), `\u0026`) {
		t.Fatalf("request escaped the url: %s", line)
	}
	request, err := ParseRequest(mustObject(t, string(line)))
	if err != nil {
		t.Fatal(err)
	}
	if request.Input["url"] != url || request.Input["dest_dir"] != "imports/example" {
		t.Fatalf("round trip = %#v", request.Input)
	}
	if _, err := buildImportRequest("bad id", url, "/var/lib/musik", "imports/example"); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("bad id err = %v", err)
	}
}

func TestProgressViewKeepsEachTrackAndRedactsTheURL(t *testing.T) {
	secret := "https://example.invalid/secret-track"
	progress, err := ParseProgress(mustObject(t, `{
		"type":"progress","protocol":"1","id":"job-1",
		"progress":{
			"pct":40,"phase":"download","message":"2 из 3",
			"items":[
				{"title":"Первая","status":"done","pct":100},
				{"title":"`+secret+`","status":"running","pct":20},
				{"title":"плохой","status":"later"},
				{"title":"","status":"pending"}
			]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	view := ProgressView(progress, secret)
	items, _ := view["items"].([]map[string]any)
	if len(items) != 2 || items[0]["title"] != "Первая" || items[0]["status"] != "done" {
		t.Fatalf("items = %#v", view["items"])
	}
	if items[1]["title"] != "[redacted]" || items[1]["status"] != "running" || items[1]["pct"] != 20.0 {
		t.Fatalf("running item = %#v", items[1])
	}
	if strings.Contains(fmt.Sprint(view), secret) {
		t.Fatalf("secret leaked: %#v", view)
	}
}

func TestProgressAndResultValidation(t *testing.T) {
	if _, err := ParseProgress(mustObject(t, `{"type":"progress","protocol":"1","id":"job-1","progress":{}}`)); err == nil {
		t.Fatal("empty progress accepted")
	}
	if _, err := ParseProgress(mustObject(t, `{"type":"progress","protocol":"1","id":"job-1","progress":{"pct":true}}`)); err == nil {
		t.Fatal("bool pct accepted")
	}
	if _, err := ParseResult(mustObject(t, `{"type":"result","protocol":"1","id":"job-1","result":{"paths":["../x.mp3"]}}`)); err == nil {
		t.Fatal("parent path accepted")
	}
	if _, err := ParseError(mustObject(t, `{"type":"error","protocol":"1","id":"job-1","error":{"code":"Bad","message":"no"}}`)); err == nil {
		t.Fatal("bad code accepted")
	}
	described, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"example","capabilities":["library.import_url"],"vendor":{"channel":"stable"}}`))
	if err != nil {
		t.Fatal(err)
	}
	vendor, _ := described.Extra["vendor"].(map[string]any)
	if vendor["channel"] != "stable" {
		t.Fatalf("vendor = %#v", described.Extra["vendor"])
	}
}

func TestRedact(t *testing.T) {
	secret := "https://example.invalid/secret"
	if got := Redact("see "+secret+" now", secret); got != "see [redacted] now" {
		t.Fatalf("redact = %q", got)
	}
	if got := Redact("plain", ""); got != "plain" {
		t.Fatalf("empty secret = %q", got)
	}
}
