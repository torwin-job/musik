package plugins

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	argDescribe = "describe"
	argRun      = "run"
)

var errProtocolStop = errors.New("protocol stop")

// Limits bounds how plugin processes are started and stopped.
// Zero values select defaults.
type Limits struct {
	Concurrency     int
	Timeout         time.Duration
	SearchTimeout   time.Duration
	DescribeTimeout time.Duration
	KillGrace       time.Duration
	MaxStderr       int
}

// Runner starts plugin processes and limits how many run at once.
type Runner struct {
	limits Limits
	slots  chan struct{}
}

// NewRunner returns a runner with limits applied. Zero fields use defaults.
func NewRunner(limits Limits) *Runner {
	if limits.Concurrency <= 0 {
		limits.Concurrency = 2
	}
	if limits.Timeout <= 0 {
		limits.Timeout = 15 * time.Minute
	}
	if limits.SearchTimeout <= 0 {
		limits.SearchTimeout = 30 * time.Second
	}
	if limits.DescribeTimeout <= 0 {
		limits.DescribeTimeout = 10 * time.Second
	}
	if limits.KillGrace <= 0 {
		limits.KillGrace = 5 * time.Second
	}
	if limits.MaxStderr <= 0 {
		limits.MaxStderr = 64 << 10
	}
	slots := make(chan struct{}, limits.Concurrency)
	for i := 0; i < limits.Concurrency; i++ {
		slots <- struct{}{}
	}
	return &Runner{limits: limits, slots: slots}
}

// ImportJob is one library.import_url task.
// OnProgress is called from the stdout reader and must return.
// The import URL is not written to logs.
type ImportJob struct {
	ID         string
	URL        string
	Library    string
	DestDir    string
	OnProgress func(Progress)
}

// ImportResult holds library-relative paths that resolved inside the destination.
type ImportResult struct {
	Paths    []string
	Absolute []string
}

// Describe runs `<executable> describe` and validates the document.
func (r *Runner) Describe(ctx context.Context, executable string) (Describe, error) {
	if err := ctx.Err(); err != nil {
		return Describe{}, err
	}
	if err := checkExecutable(executable); err != nil {
		return Describe{}, err
	}
	runCtx, stop := context.WithTimeout(ctx, r.limits.DescribeTimeout)
	defer stop()
	if err := r.acquire(runCtx); err != nil {
		if ctx.Err() != nil {
			return Describe{}, ctx.Err()
		}
		return Describe{}, &Failure{Code: CodeInternal, Message: "plugin timed out", ExitCode: -1, cause: err}
	}
	defer r.release()

	proc, err := startProcess(executable, argDescribe, "", r.limits.KillGrace)
	if err != nil {
		return Describe{}, &Failure{Code: CodeInternal, Message: "executable failed", ExitCode: -1}
	}
	_ = proc.stdin.Close()
	var stdout bytes.Buffer
	var tooBig bool
	stderr, exit, cause := r.wait(runCtx, proc, "", func(reader io.Reader, abort func(error)) {
		n, _ := io.Copy(&stdout, io.LimitReader(reader, int64(maxLineBytes)+1))
		if n > int64(maxLineBytes) {
			tooBig = true
			abort(errProtocolStop)
		}
	})
	if ctx.Err() != nil && (errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
		return Describe{}, ctx.Err()
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		return Describe{}, &Failure{
			Code: CodeInternal, Message: "plugin timed out", ExitCode: exit, Stderr: stderr, cause: cause,
		}
	}
	if tooBig || len(stdout.Bytes()) > maxLineBytes {
		return Describe{}, &Failure{Code: CodeInternal, Message: "plugin protocol is invalid", ExitCode: exit, Stderr: stderr}
	}
	if exit != 0 {
		return Describe{}, &Failure{Code: CodeInternal, Message: "describe failed", ExitCode: exit, Stderr: stderr}
	}
	payload, err := decodeObject(stdout.Bytes())
	if err != nil {
		return Describe{}, &Failure{Code: CodeInternal, Message: "plugin protocol is invalid", ExitCode: exit, Stderr: stderr}
	}
	described, err := ParseDescribe(payload)
	if err != nil {
		var fail *Failure
		if errors.As(err, &fail) {
			fail.ExitCode = exit
			fail.Stderr = stderr
		}
		return Describe{}, err
	}
	return described, nil
}

// ImportURL runs plugin once for a library.import_url task.
func (r *Runner) ImportURL(ctx context.Context, plugin Plugin, job ImportJob) (ImportResult, error) {
	if err := ctx.Err(); err != nil {
		return ImportResult{}, cancelled(plugin.ID, job.ID, err)
	}
	if !hasCapability(plugin, CapabilityImportURL) {
		return ImportResult{}, &Failure{
			Code: CodeUnsupportedAction, Message: "unsupported action",
			PluginID: plugin.ID, JobID: job.ID, ExitCode: -1,
		}
	}
	if !validPluginID(plugin.ID) {
		return ImportResult{}, tag(failure(CodeInvalidRequest, "plugin id is invalid"), plugin.ID, job.ID)
	}
	dest := job.DestDir
	if dest == "" {
		dest = "imports/" + plugin.ID
	}
	library, err := normalizeLibrary(job.Library)
	if err != nil {
		return ImportResult{}, tag(err, plugin.ID, job.ID)
	}
	line, err := buildImportRequest(job.ID, job.URL, library, dest)
	if err != nil {
		return ImportResult{}, tag(err, plugin.ID, job.ID)
	}
	if err := checkExecutable(plugin.Executable); err != nil {
		return ImportResult{}, tag(err, plugin.ID, job.ID)
	}

	runCtx, stop := context.WithTimeout(ctx, r.limits.Timeout)
	defer stop()
	if err := r.acquire(runCtx); err != nil {
		return ImportResult{}, signalFailure(plugin.ID, job.ID, err)
	}
	defer r.release()

	proc, err := startProcess(plugin.Executable, argRun, job.URL, r.limits.KillGrace)
	if err != nil {
		return ImportResult{}, tag(failure(CodeInternal, "executable failed"), plugin.ID, job.ID)
	}
	if _, err := proc.stdin.Write(line); err != nil {
		_ = proc.stdin.Close()
		_, _, _ = r.wait(runCtx, proc, job.URL, func(reader io.Reader, _ func(error)) {
			_, _ = io.Copy(io.Discard, reader)
		})
		return ImportResult{}, &Failure{
			Code: CodeInternal, Message: "executable failed",
			PluginID: plugin.ID, JobID: job.ID, ExitCode: -1,
		}
	}
	_ = proc.stdin.Close()

	collected := &outcome{}
	stderr, exit, cause := r.wait(runCtx, proc, job.URL, func(reader io.Reader, abort func(error)) {
		readRunStdout(reader, job, runCtx, collected, abort)
	})
	return interpretRun(plugin, job, library, dest, collected, stderr, exit, cause)
}

// SearchJob is one library.search task.
// The query is written to the plugin stdin and is not placed in argv or logs.
type SearchJob struct {
	ID    string
	Query string
	Limit int
}

// Search runs plugin once for a library.search task.
// Cancelling ctx sends SIGTERM. The run also stops after SearchTimeout.
func (r *Runner) Search(ctx context.Context, plugin Plugin, job SearchJob) ([]SearchItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, cancelled(plugin.ID, job.ID, err)
	}
	if !hasCapability(plugin, CapabilitySearch) {
		return nil, &Failure{
			Code: CodeUnsupportedAction, Message: "unsupported action",
			PluginID: plugin.ID, JobID: job.ID, ExitCode: -1,
		}
	}
	if !validPluginID(plugin.ID) {
		return nil, tag(failure(CodeInvalidRequest, "plugin id is invalid"), plugin.ID, job.ID)
	}
	if job.Limit < MinSearchLimit || job.Limit > MaxSearchLimit {
		return nil, tag(failure(CodeInvalidRequest, "limit is invalid"), plugin.ID, job.ID)
	}
	line, err := buildSearchRequest(job.ID, job.Query, job.Limit)
	if err != nil {
		return nil, tag(err, plugin.ID, job.ID)
	}
	if err := checkExecutable(plugin.Executable); err != nil {
		return nil, tag(err, plugin.ID, job.ID)
	}

	runCtx, stop := context.WithTimeout(ctx, r.limits.SearchTimeout)
	defer stop()
	if err := r.acquire(runCtx); err != nil {
		return nil, signalFailure(plugin.ID, job.ID, err)
	}
	defer r.release()

	proc, err := startProcess(plugin.Executable, argRun, job.Query, r.limits.KillGrace)
	if err != nil {
		return nil, tag(failure(CodeInternal, "executable failed"), plugin.ID, job.ID)
	}
	if _, err := proc.stdin.Write(line); err != nil {
		_ = proc.stdin.Close()
		_, _, _ = r.wait(runCtx, proc, job.Query, func(reader io.Reader, _ func(error)) {
			_, _ = io.Copy(io.Discard, reader)
		})
		return nil, &Failure{
			Code: CodeInternal, Message: "executable failed",
			PluginID: plugin.ID, JobID: job.ID, ExitCode: -1,
		}
	}
	_ = proc.stdin.Close()

	collected := &searchOutcome{}
	stderr, exit, cause := r.wait(runCtx, proc, job.Query, func(reader io.Reader, abort func(error)) {
		readSearchStdout(reader, job, runCtx, collected, abort)
	})
	return interpretSearch(plugin, job, collected, stderr, exit, cause)
}

func (r *Runner) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.slots:
		return nil
	}
}

func (r *Runner) release() {
	r.slots <- struct{}{}
}

func (r *Runner) wait(ctx context.Context, proc *running, secret string, readStdout func(io.Reader, func(error))) (stderr string, exit int, cause error) {
	procCtx, stop := context.WithCancel(ctx)
	session := &stopSession{parent: ctx, stop: stop}
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readStdout(proc.stdout, session.abort)
		_, _ = io.Copy(io.Discard, proc.stdout)
	}()
	stderrC := make(chan string, 1)
	go func() {
		defer wg.Done()
		stderrC <- readStderr(proc.stderr, r.limits.MaxStderr, secret)
	}()

	exit, cause = supervise(procCtx, proc.cmd, r.limits.KillGrace, session.reason)
	_ = proc.stdout.Close()
	_ = proc.stderr.Close()
	wg.Wait()
	return <-stderrC, exit, cause
}

func checkExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return failure(CodeInternal, "executable path is invalid")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return failure(CodeInternal, "executable is missing")
	}
	if info.Mode()&0o111 == 0 {
		return failure(CodeInternal, "executable is not runnable")
	}
	return nil
}

func tag(err error, pluginID, jobID string) error {
	var fail *Failure
	if errors.As(err, &fail) {
		if fail.PluginID == "" {
			fail.PluginID = pluginID
		}
		if fail.JobID == "" {
			fail.JobID = jobID
		}
		return fail
	}
	return err
}

func cancelled(pluginID, jobID string, cause error) *Failure {
	return &Failure{
		Code: CodeCancelled, Message: "plugin cancelled",
		PluginID: pluginID, JobID: jobID, ExitCode: -1, cause: cause,
	}
}

func signalFailure(pluginID, jobID string, cause error) *Failure {
	if errors.Is(cause, context.Canceled) {
		return cancelled(pluginID, jobID, cause)
	}
	return &Failure{
		Code: CodeInternal, Message: "plugin timed out",
		PluginID: pluginID, JobID: jobID, ExitCode: -1, cause: cause,
	}
}

func interpretRun(plugin Plugin, job ImportJob, library, dest string, collected *outcome, stderr string, exit int, cause error) (ImportResult, error) {
	result, pluginMsg, proto, handler := collected.snapshot()
	fail := func(code, message string, cause error) *Failure {
		return &Failure{
			Code: code, Message: message, PluginID: plugin.ID, JobID: job.ID,
			ExitCode: exit, Stderr: stderr, cause: cause,
		}
	}
	if handler {
		return ImportResult{}, fail(CodeInternal, "progress handler failed", nil)
	}
	if errors.Is(cause, errProtocolStop) || (cause == nil && proto) {
		return ImportResult{}, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return ImportResult{}, fail(CodeInternal, "plugin timed out", cause)
	}
	if errors.Is(cause, context.Canceled) {
		if pluginMsg != nil && exit == 1 && !proto {
			return ImportResult{}, fail(pluginMsg.Code, pluginMsg.Message, cause)
		}
		return ImportResult{}, fail(CodeCancelled, "plugin cancelled", cause)
	}
	if proto {
		return ImportResult{}, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if exit == 2 {
		return ImportResult{}, fail(CodeInternal, "plugin configuration is unusable", nil)
	}
	if result != nil && pluginMsg != nil {
		return ImportResult{}, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if result != nil {
		if exit != 0 {
			return ImportResult{}, fail(CodeInternal, "plugin exit status does not match its response", nil)
		}
		resolved, err := AcceptImportPaths(library, dest, result.Paths)
		if err != nil {
			tagged := tag(err, plugin.ID, job.ID)
			var pathErr *Failure
			if errors.As(tagged, &pathErr) {
				pathErr.Stderr = stderr
				pathErr.ExitCode = exit
			}
			return ImportResult{}, tagged
		}
		imported := ImportResult{
			Paths:    make([]string, len(resolved)),
			Absolute: make([]string, len(resolved)),
		}
		for i, path := range resolved {
			imported.Paths[i] = path.Relative
			imported.Absolute[i] = path.Absolute
		}
		return imported, nil
	}
	if pluginMsg != nil {
		if exit != 1 {
			return ImportResult{}, fail(CodeInternal, "plugin exit status does not match its response", nil)
		}
		return ImportResult{}, fail(pluginMsg.Code, pluginMsg.Message, nil)
	}
	return ImportResult{}, fail(CodeInternal, "plugin exited without a result", nil)
}

type outcome struct {
	mu       sync.Mutex
	result   *Result
	plugin   *ErrorMessage
	proto    bool
	handler  bool
	terminal bool
}

func (o *outcome) setProto() {
	o.mu.Lock()
	o.proto = true
	o.mu.Unlock()
}

func (o *outcome) setHandler() {
	o.mu.Lock()
	o.handler = true
	o.mu.Unlock()
}

func (o *outcome) setResult(result Result) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		o.proto = true
		return false
	}
	o.terminal = true
	o.result = &result
	return true
}

func (o *outcome) setPlugin(message ErrorMessage) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		o.proto = true
		return false
	}
	o.terminal = true
	o.plugin = &message
	return true
}

func (o *outcome) done() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.terminal
}

func (o *outcome) snapshot() (result *Result, plugin *ErrorMessage, proto, handler bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.result, o.plugin, o.proto, o.handler
}

type stopSession struct {
	parent context.Context
	stop   context.CancelFunc
	mu     sync.Mutex
	why    error
}

func (s *stopSession) abort(reason error) {
	if s.parent.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.why == nil {
		s.why = reason
	}
	s.mu.Unlock()
	s.stop()
}

func (s *stopSession) reason() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.why
}

func readRunStdout(r io.Reader, job ImportJob, parent context.Context, out *outcome, abort func(error)) {
	reader := bufio.NewReaderSize(r, 32<<10)
	for {
		if parent.Err() != nil {
			return
		}
		line, err := readLine(reader, maxLineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			if parent.Err() == nil {
				out.setProto()
				abort(errProtocolStop)
			}
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			out.setProto()
			abort(errProtocolStop)
			return
		}
		payload, err := decodeObject(line)
		if err != nil {
			out.setProto()
			abort(errProtocolStop)
			return
		}
		switch payload["type"] {
		case "progress":
			if out.done() {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			progress, err := ParseProgress(payload)
			if err != nil || progress.ID != job.ID {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			progress.Message = Redact(progress.Message, job.URL)
			if !safeProgress(job.OnProgress, progress, out) {
				abort(errProtocolStop)
				return
			}
		case "result":
			result, err := ParseResult(payload)
			if err != nil || result.ID != job.ID || pathsLeak(result.Paths, job.URL) {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			if !out.setResult(result) {
				abort(errProtocolStop)
				return
			}
		case "error":
			message, err := ParseError(payload)
			if err != nil || message.ID != job.ID {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			message.Message = Redact(message.Message, job.URL)
			if !out.setPlugin(message) {
				abort(errProtocolStop)
				return
			}
		default:
			out.setProto()
			abort(errProtocolStop)
			return
		}
	}
}

func safeProgress(fn func(Progress), progress Progress, out *outcome) (ok bool) {
	if fn == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			out.setHandler()
			ok = false
		}
	}()
	fn(progress)
	return true
}

type searchOutcome struct {
	mu       sync.Mutex
	result   *SearchResult
	plugin   *ErrorMessage
	proto    bool
	terminal bool
}

func (o *searchOutcome) setProto() {
	o.mu.Lock()
	o.proto = true
	o.mu.Unlock()
}

func (o *searchOutcome) setResult(result SearchResult) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		o.proto = true
		return false
	}
	o.terminal = true
	o.result = &result
	return true
}

func (o *searchOutcome) setPlugin(message ErrorMessage) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		o.proto = true
		return false
	}
	o.terminal = true
	o.plugin = &message
	return true
}

func (o *searchOutcome) snapshot() (result *SearchResult, plugin *ErrorMessage, proto bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.result, o.plugin, o.proto
}

func interpretSearch(plugin Plugin, job SearchJob, collected *searchOutcome, stderr string, exit int, cause error) ([]SearchItem, error) {
	result, pluginMsg, proto := collected.snapshot()
	fail := func(code, message string, cause error) *Failure {
		return &Failure{
			Code: code, Message: Redact(message, job.Query), PluginID: plugin.ID, JobID: job.ID,
			ExitCode: exit, Stderr: stderr, cause: cause,
		}
	}
	if errors.Is(cause, errProtocolStop) || (cause == nil && proto) {
		return nil, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return nil, fail(CodeInternal, "plugin timed out", cause)
	}
	if errors.Is(cause, context.Canceled) {
		if pluginMsg != nil && exit == 1 && !proto {
			return nil, fail(pluginMsg.Code, pluginMsg.Message, cause)
		}
		return nil, fail(CodeCancelled, "plugin cancelled", cause)
	}
	if proto {
		return nil, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if exit == 2 {
		return nil, fail(CodeInternal, "plugin configuration is unusable", nil)
	}
	if result != nil && pluginMsg != nil {
		return nil, fail(CodeInternal, "plugin protocol is invalid", nil)
	}
	if result != nil {
		if exit != 0 {
			return nil, fail(CodeInternal, "plugin exit status does not match its response", nil)
		}
		if result.Items == nil {
			return []SearchItem{}, nil
		}
		return result.Items, nil
	}
	if pluginMsg != nil {
		if exit != 1 {
			return nil, fail(CodeInternal, "plugin exit status does not match its response", nil)
		}
		return nil, fail(pluginMsg.Code, pluginMsg.Message, nil)
	}
	return nil, fail(CodeInternal, "plugin exited without a result", nil)
}

func readSearchStdout(r io.Reader, job SearchJob, parent context.Context, out *searchOutcome, abort func(error)) {
	reader := bufio.NewReaderSize(r, 32<<10)
	for {
		if parent.Err() != nil {
			return
		}
		line, err := readLine(reader, maxLineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			if parent.Err() == nil {
				out.setProto()
				abort(errProtocolStop)
			}
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			out.setProto()
			abort(errProtocolStop)
			return
		}
		payload, err := decodeObject(line)
		if err != nil {
			out.setProto()
			abort(errProtocolStop)
			return
		}
		switch payload["type"] {
		case "progress":
			if out.terminalLocked() {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			progress, err := ParseProgress(payload)
			if err != nil || progress.ID != job.ID {
				out.setProto()
				abort(errProtocolStop)
				return
			}
		case "result":
			result, err := ParseSearchResult(payload)
			if err != nil || result.ID != job.ID {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			if !out.setResult(result) {
				abort(errProtocolStop)
				return
			}
		case "error":
			message, err := ParseError(payload)
			if err != nil || message.ID != job.ID {
				out.setProto()
				abort(errProtocolStop)
				return
			}
			message.Message = Redact(message.Message, job.Query)
			if !out.setPlugin(message) {
				abort(errProtocolStop)
				return
			}
		default:
			out.setProto()
			abort(errProtocolStop)
			return
		}
	}
}

func (o *searchOutcome) terminalLocked() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.terminal
}

func pathsLeak(paths []string, secret string) bool {
	if len(secret) < 8 {
		return false
	}
	for _, path := range paths {
		if strings.Contains(path, secret) {
			return true
		}
	}
	return false
}
