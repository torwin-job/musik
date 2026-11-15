package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	PluginJobPending   = "pending"
	PluginJobRunning   = "running"
	PluginJobDone      = "done"
	PluginJobError     = "error"
	PluginJobCancelled = "cancelled"
)

// PluginJob is one external-plugin task. Input holds the import URL and is
// not part of the public API response; callers that serialize a job for a
// client must leave it out.
type PluginJob struct {
	ID              int64  `json:"id"`
	PluginID        string `json:"plugin_id"`
	Action          string `json:"action"`
	Input           string `json:"-"`
	Status          string `json:"status"`
	Progress        string `json:"progress_json,omitempty"`
	Result          string `json:"result_json,omitempty"`
	Error           string `json:"error,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	DownstreamJobID int64  `json:"downstream_job_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func (s *Store) CreatePluginJob(pluginID, action, inputJSON string) (PluginJob, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var row PluginJobRecord
	err := s.DB.Get(context.Background(), &row, `
INSERT INTO plugin_jobs(plugin_id, action, input_json, status, created_at, updated_at)
VALUES (?, ?, ?, '`+PluginJobPending+`', ?, ?)
RETURNING `+pluginJobColumns, pluginID, action, inputJSON, now, now)
	if err != nil {
		return PluginJob{}, err
	}
	return mapPluginJob(row), nil
}

func (s *Store) GetPluginJob(id int64) (*PluginJob, error) {
	var row PluginJobRecord
	err := s.DB.Get(context.Background(), &row, `SELECT `+pluginJobColumns+` FROM plugin_jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job := mapPluginJob(row)
	return &job, nil
}

func (s *Store) ListPluginJobs(status string, limit int) ([]PluginJob, error) {
	if limit < 1 {
		limit = 50
	}
	query := `SELECT ` + pluginJobColumns + ` FROM plugin_jobs`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	var rows []PluginJobRecord
	if err := s.DB.Select(context.Background(), &rows, query, args...); err != nil {
		return nil, err
	}
	return mapPluginJobs(rows), nil
}

// MarkPluginJobRunning moves a pending job to running. The boolean is false
// when the row was already claimed or finished.
func (s *Store) MarkPluginJobRunning(id int64) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(
		`UPDATE plugin_jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		PluginJobRunning, now, id, PluginJobPending,
	)
	return oneRow(res, err)
}

// SetPluginJobProgress records the latest progress line while the job is running.
// A finished job keeps the progress it had at the terminal update.
func (s *Store) SetPluginJobProgress(id int64, progressJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(
		`UPDATE plugin_jobs SET progress_json = ?, updated_at = ? WHERE id = ? AND status = ?`,
		progressJSON, now, id, PluginJobRunning,
	)
	return err
}

// FinishPluginJob records a terminal status from pending or running.
// The boolean is false when the row was already terminal.
func (s *Store) FinishPluginJob(id int64, status, resultJSON, errorCode, errorText string) (bool, error) {
	return s.finishPluginJob(id, status, resultJSON, errorCode, errorText, 0)
}

// FinishPluginImport marks a successful import done and records the catalog
// job in the same update. downstreamID 0 leaves the link empty.
func (s *Store) FinishPluginImport(id int64, resultJSON string, downstreamID int64) (bool, error) {
	return s.finishPluginJob(id, PluginJobDone, resultJSON, "", "", downstreamID)
}

func (s *Store) finishPluginJob(id int64, status, resultJSON, errorCode, errorText string, downstreamID int64) (bool, error) {
	switch status {
	case PluginJobDone, PluginJobError, PluginJobCancelled:
	default:
		return false, fmt.Errorf("invalid plugin job status %q", status)
	}
	var downstream any
	if downstreamID > 0 {
		downstream = downstreamID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(`
UPDATE plugin_jobs
SET status = ?, result_json = ?, error_code = ?, error = ?, downstream_job_id = COALESCE(?, downstream_job_id), updated_at = ?
WHERE id = ? AND status IN (?, ?)`,
		status, nullStr(resultJSON), nullStr(errorCode), nullStr(errorText), downstream, now,
		id, PluginJobPending, PluginJobRunning,
	)
	return oneRow(res, err)
}

// SetPluginJobDownstream attaches the catalog job created after a successful import.
func (s *Store) SetPluginJobDownstream(id, downstreamID int64) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(`
UPDATE plugin_jobs
SET downstream_job_id = ?, updated_at = ?
WHERE id = ? AND status = ? AND downstream_job_id IS NULL`,
		downstreamID, now, id, PluginJobDone,
	)
	return oneRow(res, err)
}

// InterruptPluginJobs fails tasks that were still open when the process stopped.
// Their plugin process is gone, so leaving them running would never finish.
func (s *Store) InterruptPluginJobs() error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
UPDATE plugin_jobs
SET status = ?, error_code = ?, error = ?, updated_at = ?
WHERE status IN (?, ?)`,
		PluginJobError, "internal", "plugin interrupted", now,
		PluginJobPending, PluginJobRunning,
	)
	return err
}

func oneRow(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func mapPluginJob(row PluginJobRecord) PluginJob {
	job := PluginJob{
		ID: row.ID, PluginID: row.PluginID, Action: row.Action, Input: row.InputJSON,
		Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.ProgressJSON != nil {
		job.Progress = *row.ProgressJSON
	}
	if row.ResultJSON != nil {
		job.Result = *row.ResultJSON
	}
	if row.Error != nil {
		job.Error = *row.Error
	}
	if row.ErrorCode != nil {
		job.ErrorCode = *row.ErrorCode
	}
	if row.DownstreamJobID != nil {
		job.DownstreamJobID = *row.DownstreamJobID
	}
	return job
}

func mapPluginJobs(rows []PluginJobRecord) []PluginJob {
	out := make([]PluginJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapPluginJob(row))
	}
	return out
}
