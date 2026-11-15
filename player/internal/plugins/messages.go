package plugins

// Describe is the document written by `<executable> describe`.
type Describe struct {
	ID           string
	Capabilities []string
	Name         string
	Version      string
	Description  string
	Extra        map[string]any
}

// Request is one JSON Lines request. Input and Extra keep unknown properties.
type Request struct {
	ID     string
	Action string
	Input  map[string]any
	Extra  map[string]any
}

// Progress is one progress line. Pct is nil when the plugin omitted it.
type Progress struct {
	ID            string
	Pct           *float64
	Phase         string
	Message       string
	Extra         map[string]any
	ProgressExtra map[string]any
}

// Result is a successful terminal line.
type Result struct {
	ID          string
	Paths       []string
	Extra       map[string]any
	ResultExtra map[string]any
}

// ErrorMessage is a failed terminal line.
type ErrorMessage struct {
	ID         string
	Code       string
	Message    string
	Extra      map[string]any
	ErrorExtra map[string]any
}

// ParseDescribe validates a describe document.
func ParseDescribe(payload map[string]any) (Describe, error) {
	if payload == nil {
		return Describe{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "describe" {
		return Describe{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return Describe{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validPluginID(id) {
		return Describe{}, failure(CodeInvalidRequest, "plugin id is invalid")
	}
	capabilities, ok := capabilityList(payload["capabilities"])
	if !ok {
		return Describe{}, failure(CodeInvalidRequest, "capabilities are invalid")
	}
	name, ok := optionalText(payload, "name", maxNameChars)
	if !ok {
		return Describe{}, failure(CodeInvalidRequest, "message field is invalid")
	}
	version, ok := optionalText(payload, "version", maxVersionChars)
	if !ok {
		return Describe{}, failure(CodeInvalidRequest, "message field is invalid")
	}
	description, ok := optionalText(payload, "description", maxDescriptionChars)
	if !ok {
		return Describe{}, failure(CodeInvalidRequest, "message field is invalid")
	}
	return Describe{
		ID:           id,
		Capabilities: capabilities,
		Name:         name,
		Version:      version,
		Description:  description,
		Extra:        extraFields(payload, "type", "protocol", "id", "capabilities", "name", "version", "description"),
	}, nil
}

// ParseRequest validates a request line without binding an action.
func ParseRequest(payload map[string]any) (Request, error) {
	if payload == nil {
		return Request{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "request" {
		return Request{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return Request{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validRequestID(id) {
		return Request{}, failure(CodeInvalidRequest, "request id is invalid")
	}
	action, ok := stringField(payload, "action")
	if !ok || !validCapability(action) {
		return Request{}, failure(CodeInvalidRequest, "action is invalid")
	}
	input, ok := payload["input"].(map[string]any)
	if !ok {
		return Request{}, failure(CodeInvalidRequest, "input is invalid")
	}
	return Request{
		ID:     id,
		Action: action,
		Input:  input,
		Extra:  extraFields(payload, "type", "protocol", "id", "action", "input"),
	}, nil
}

// ParseProgress validates a progress line.
func ParseProgress(payload map[string]any) (Progress, error) {
	if payload == nil {
		return Progress{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "progress" {
		return Progress{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return Progress{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validRequestID(id) {
		return Progress{}, failure(CodeInvalidRequest, "request id is invalid")
	}
	body, ok := payload["progress"].(map[string]any)
	if !ok {
		return Progress{}, failure(CodeInvalidRequest, "progress is invalid")
	}
	progress := Progress{
		ID:            id,
		Extra:         extraFields(payload, "type", "protocol", "id", "progress"),
		ProgressExtra: extraFields(body, "pct", "phase", "message"),
	}
	if raw, ok := body["pct"]; ok {
		pct, ok := numberField(raw)
		if !ok || pct < 0 || pct > 100 {
			return Progress{}, failure(CodeInvalidRequest, "progress is invalid")
		}
		progress.Pct = &pct
	}
	if raw, ok := body["phase"]; ok {
		phase, ok := raw.(string)
		if !ok || !phasePattern.MatchString(phase) {
			return Progress{}, failure(CodeInvalidRequest, "progress is invalid")
		}
		progress.Phase = phase
	}
	if raw, ok := body["message"]; ok {
		message, ok := raw.(string)
		if !ok || !validText(message, maxProgressMessage) {
			return Progress{}, failure(CodeInvalidRequest, "progress is invalid")
		}
		progress.Message = message
	}
	if progress.Pct == nil && progress.Phase == "" && progress.Message == "" {
		return Progress{}, failure(CodeInvalidRequest, "progress is invalid")
	}
	return progress, nil
}

// ParseResult validates a result line. Paths are grammatical only; the runner
// checks that the files exist inside the library.
func ParseResult(payload map[string]any) (Result, error) {
	if payload == nil {
		return Result{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "result" {
		return Result{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return Result{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validRequestID(id) {
		return Result{}, failure(CodeInvalidRequest, "request id is invalid")
	}
	body, ok := payload["result"].(map[string]any)
	if !ok {
		return Result{}, failure(CodeInvalidRequest, "result is invalid")
	}
	rawPaths, ok := body["paths"].([]any)
	if !ok || len(rawPaths) < 1 || len(rawPaths) > maxResultPaths {
		return Result{}, failure(CodeInvalidRequest, "result paths are invalid")
	}
	paths := make([]string, 0, len(rawPaths))
	for _, item := range rawPaths {
		text, ok := item.(string)
		if !ok || !validRelativePath(text) {
			return Result{}, failure(CodeInvalidRequest, "result paths are invalid")
		}
		paths = append(paths, text)
	}
	return Result{
		ID:          id,
		Paths:       paths,
		Extra:       extraFields(payload, "type", "protocol", "id", "result"),
		ResultExtra: extraFields(body, "paths"),
	}, nil
}

// ParseError validates an error line.
func ParseError(payload map[string]any) (ErrorMessage, error) {
	if payload == nil {
		return ErrorMessage{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "error" {
		return ErrorMessage{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return ErrorMessage{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validRequestID(id) {
		return ErrorMessage{}, failure(CodeInvalidRequest, "request id is invalid")
	}
	body, ok := payload["error"].(map[string]any)
	if !ok {
		return ErrorMessage{}, failure(CodeInvalidRequest, "error is invalid")
	}
	code, ok := stringField(body, "code")
	if !ok || !errorCodePattern.MatchString(code) {
		return ErrorMessage{}, failure(CodeInvalidRequest, "error is invalid")
	}
	message, ok := stringField(body, "message")
	if !ok || !validText(message, maxErrorMessage) {
		return ErrorMessage{}, failure(CodeInvalidRequest, "error is invalid")
	}
	return ErrorMessage{
		ID:         id,
		Code:       code,
		Message:    message,
		Extra:      extraFields(payload, "type", "protocol", "id", "error"),
		ErrorExtra: extraFields(body, "code", "message"),
	}, nil
}

type requestLine struct {
	Type     string         `json:"type"`
	Protocol string         `json:"protocol"`
	ID       string         `json:"id"`
	Action   string         `json:"action"`
	Input    importURLInput `json:"input"`
}

type importURLInput struct {
	URL     string `json:"url"`
	Library string `json:"library"`
	DestDir string `json:"dest_dir"`
}

// ValidImportURL reports whether url can be sent as a library.import_url input.
func ValidImportURL(url string) bool {
	return validText(url, maxURLChars)
}

func buildImportRequest(id, url, library, destDir string) ([]byte, error) {
	if !validRequestID(id) {
		return nil, failure(CodeInvalidRequest, "request id is invalid")
	}
	if !validText(url, maxURLChars) {
		return nil, failure(CodeInvalidRequest, "url is invalid")
	}
	if !validLibraryPath(library) {
		return nil, failure(CodeInvalidRequest, "library path is invalid")
	}
	if !validRelativePath(destDir) {
		return nil, failure(CodeInvalidRequest, "destination is invalid")
	}
	return encodeLine(requestLine{
		Type:     "request",
		Protocol: ProtocolVersion,
		ID:       id,
		Action:   CapabilityImportURL,
		Input: importURLInput{
			URL:     url,
			Library: library,
			DestDir: destDir,
		},
	})
}

// SearchItem is one library.search row. URL is an opaque string for a later import.
type SearchItem struct {
	Title    string
	Artist   string
	URL      string
	Duration *int
	Extra    map[string]any
}

// SearchResult is a successful library.search line.
type SearchResult struct {
	ID          string
	Items       []SearchItem
	Extra       map[string]any
	ResultExtra map[string]any
}

// ValidSearchQuery reports whether query can be sent as a library.search input.
func ValidSearchQuery(query string) bool {
	return validText(query, maxQueryChars)
}

// ParseSearchResult validates a library.search result. Item URLs are grammatical
// text only; the host does not inspect the address.
func ParseSearchResult(payload map[string]any) (SearchResult, error) {
	if payload == nil {
		return SearchResult{}, failure(CodeInvalidRequest, "malformed request")
	}
	if payload["type"] != "result" {
		return SearchResult{}, failure(CodeInvalidRequest, "message type is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return SearchResult{}, err
	}
	id, ok := stringField(payload, "id")
	if !ok || !validRequestID(id) {
		return SearchResult{}, failure(CodeInvalidRequest, "request id is invalid")
	}
	body, ok := payload["result"].(map[string]any)
	if !ok {
		return SearchResult{}, failure(CodeInvalidRequest, "result is invalid")
	}
	rawItems, ok := body["items"].([]any)
	if !ok || len(rawItems) > maxSearchItems {
		return SearchResult{}, failure(CodeInvalidRequest, "search items are invalid")
	}
	items := make([]SearchItem, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return SearchResult{}, failure(CodeInvalidRequest, "search items are invalid")
		}
		parsed, err := parseSearchItem(item)
		if err != nil {
			return SearchResult{}, err
		}
		items = append(items, parsed)
	}
	return SearchResult{
		ID:          id,
		Items:       items,
		Extra:       extraFields(payload, "type", "protocol", "id", "result"),
		ResultExtra: extraFields(body, "items"),
	}, nil
}

func parseSearchItem(item map[string]any) (SearchItem, error) {
	title, ok := stringField(item, "title")
	if !ok || !validText(title, maxItemText) {
		return SearchItem{}, failure(CodeInvalidRequest, "search items are invalid")
	}
	url, ok := stringField(item, "url")
	if !ok || !ValidImportURL(url) {
		return SearchItem{}, failure(CodeInvalidRequest, "search items are invalid")
	}
	parsed := SearchItem{
		Title: title,
		URL:   url,
		Extra: extraFields(item, "title", "artist", "url", "duration"),
	}
	if _, present := item["artist"]; present {
		artist, ok := stringField(item, "artist")
		if !ok || !validText(artist, maxItemText) {
			return SearchItem{}, failure(CodeInvalidRequest, "search items are invalid")
		}
		parsed.Artist = artist
	}
	if raw, present := item["duration"]; present {
		duration, ok := wholeNumber(raw, 0, maxDuration)
		if !ok {
			return SearchItem{}, failure(CodeInvalidRequest, "search items are invalid")
		}
		parsed.Duration = &duration
	}
	return parsed, nil
}

type searchRequestLine struct {
	Type     string      `json:"type"`
	Protocol string      `json:"protocol"`
	ID       string      `json:"id"`
	Action   string      `json:"action"`
	Input    searchInput `json:"input"`
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func buildSearchRequest(id, query string, limit int) ([]byte, error) {
	if !validRequestID(id) {
		return nil, failure(CodeInvalidRequest, "request id is invalid")
	}
	if !ValidSearchQuery(query) {
		return nil, failure(CodeInvalidRequest, "query is invalid")
	}
	if limit < MinSearchLimit || limit > MaxSearchLimit {
		return nil, failure(CodeInvalidRequest, "limit is invalid")
	}
	return encodeLine(searchRequestLine{
		Type:     "request",
		Protocol: ProtocolVersion,
		ID:       id,
		Action:   CapabilitySearch,
		Input: searchInput{
			Query: query,
			Limit: limit,
		},
	})
}
