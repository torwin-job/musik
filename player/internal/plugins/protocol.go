package plugins

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ProtocolVersion     = "1"
	CapabilityImportURL = "library.import_url"
	CapabilitySearch    = "library.search"
	DefaultSearchLimit  = 8
	MinSearchLimit      = 1
	MaxSearchLimit      = 20
	maxLineBytes        = 1 << 20
	maxManifestBytes    = 1 << 20
	maxCapabilities     = 32
	maxResultPaths      = 100
	maxPathChars        = 1024
	maxPluginIDChars    = 63
	maxExecutableChars  = 4096
	maxLibraryChars     = 4096
	maxURLChars         = 4096
	maxErrorMessage     = 500
	maxProgressMessage  = 200
	maxNameChars        = 120
	maxVersionChars     = 64
	maxDescriptionChars = 300
	maxQueryChars       = 500
	maxSearchItems      = 20
	maxItemText         = 500
	maxDuration         = 864000
	redacted            = "[redacted]"
)

var progressItemStatus = map[string]struct{}{
	"pending": {},
	"running": {},
	"done":    {},
	"error":   {},
}

var (
	pluginIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$`)
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	phasePattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	errorCodePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	relativePattern   = regexp.MustCompile(relativePathPattern())
	dotSegmentPattern = regexp.MustCompile(`(^|/)\.\.?(?:/|$)`)
	partSuffixPattern = regexp.MustCompile(`\.part$`)
	libraryPattern    = regexp.MustCompile(libraryPathPattern())

	errLineTooLong  = errors.New("line is too long")
	errPartialLine  = errors.New("line is incomplete")
	errNotObject    = errors.New("json is not an object")
	errTrailingJSON = errors.New("trailing json")
)

func relativePathPattern() string {
	ctrl := "\x00-\x1f"
	return `^[^/\\` + ctrl + `]+(?:/[^/\\` + ctrl + `]+)*$`
}

func libraryPathPattern() string {
	return `^/[^\x00-\x1f]*$`
}

// ProgressView is the progress object stored for the site. Item titles are
// redacted with the import URL. Unknown item fields are dropped.
func ProgressView(update Progress, secret string) map[string]any {
	body := map[string]any{}
	if update.Pct != nil {
		body["pct"] = *update.Pct
	}
	if update.Phase != "" {
		body["phase"] = update.Phase
	}
	if update.Message != "" {
		body["message"] = Redact(update.Message, secret)
	}
	if items := progressItems(update.ProgressExtra, secret); len(items) > 0 {
		body["items"] = items
	}
	return body
}

func progressItems(extra map[string]any, secret string) []map[string]any {
	if extra == nil {
		return nil
	}
	raw, ok := extra["items"].([]any)
	if !ok {
		return nil
	}
	if len(raw) > maxResultPaths {
		raw = raw[:maxResultPaths]
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, ok := row["title"].(string)
		if !ok || !validText(title, maxProgressMessage) {
			continue
		}
		status, ok := row["status"].(string)
		if _, known := progressItemStatus[status]; !ok || !known {
			continue
		}
		view := map[string]any{
			"title":  Redact(title, secret),
			"status": status,
		}
		if pct, ok := progressPct(row["pct"]); ok {
			view["pct"] = pct
		}
		out = append(out, view)
	}
	return out
}

func progressPct(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 100 {
		return 0, false
	}
	return number, true
}

// Redact replaces every occurrence of secret with a placeholder.
// An empty secret leaves text unchanged.
func Redact(text, secret string) string {
	if secret == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, redacted)
}

func runeCount(s string) int {
	return utf8.RuneCountInString(s)
}

func validText(s string, max int) bool {
	if s == "" || runeCount(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 {
			return false
		}
	}
	return true
}

func validPluginID(s string) bool {
	return pluginIDPattern.MatchString(s)
}

func validCapability(s string) bool {
	return capabilityPattern.MatchString(s)
}

func validRequestID(s string) bool {
	return requestIDPattern.MatchString(s)
}

func validRelativePath(s string) bool {
	if s == "" || runeCount(s) > maxPathChars {
		return false
	}
	if !relativePattern.MatchString(s) || dotSegmentPattern.MatchString(s) || partSuffixPattern.MatchString(s) {
		return false
	}
	return true
}

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(data)))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errNotObject
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errTrailingJSON
		}
		return nil, err
	}
	return object, nil
}

func encodeLine(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	if buf.Len() > maxLineBytes {
		return nil, errLineTooLong
	}
	return buf.Bytes(), nil
}

func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(buf)+len(frag) > max {
			return nil, errLineTooLong
		}
		buf = append(buf, frag...)
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, errPartialLine
		default:
			return nil, err
		}
	}
}

func extraFields(payload map[string]any, known ...string) map[string]any {
	skip := make(map[string]struct{}, len(known))
	for _, key := range known {
		skip[key] = struct{}{}
	}
	var out map[string]any
	for key, value := range payload {
		if _, ok := skip[key]; ok {
			continue
		}
		if out == nil {
			out = make(map[string]any)
		}
		out[key] = value
	}
	return out
}

func stringField(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func optionalText(payload map[string]any, key string, max int) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", true
	}
	text, ok := value.(string)
	if !ok || !validText(text, max) {
		return "", false
	}
	return text, true
}

func wholeNumber(value any, min, max int) (int, bool) {
	f, ok := numberField(value)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if f < float64(min) || f > float64(max) || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

func numberField(value any) (float64, bool) {
	switch n := value.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func capabilityList(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) < 1 || len(items) > maxCapabilities {
		return nil, false
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok || !validCapability(name) {
			return nil, false
		}
		if _, dup := seen[name]; dup {
			return nil, false
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, true
}
