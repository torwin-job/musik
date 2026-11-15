package db

import (
	"testing"
)

func TestPluginJobLifecycle(t *testing.T) {
	store, _ := openTestStore(t)
	secret := "https://example.invalid/musik-plugin-secret"
	created, err := store.CreatePluginJob("example", "library.import_url", `{"url":"`+secret+`","dest_dir":"imports/example"}`)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != PluginJobPending || created.Input == "" || created.ID == 0 {
		t.Fatalf("created = %+v", created)
	}

	running, err := store.MarkPluginJobRunning(created.ID)
	if err != nil || !running {
		t.Fatalf("mark running = %v, %v", running, err)
	}
	again, err := store.MarkPluginJobRunning(created.ID)
	if err != nil || again {
		t.Fatalf("second mark = %v, %v", again, err)
	}
	if err := store.SetPluginJobProgress(created.ID, `{"pct":40,"phase":"download","message":"[redacted]"}`); err != nil {
		t.Fatal(err)
	}

	ok, err := store.FinishPluginJob(created.ID, PluginJobDone, `{"paths":["imports/example/track.flac"]}`, "", "")
	if err != nil || !ok {
		t.Fatalf("finish = %v, %v", ok, err)
	}
	if err := store.SetPluginJobProgress(created.ID, `{"pct":90}`); err != nil {
		t.Fatal(err)
	}
	second, err := store.FinishPluginJob(created.ID, PluginJobError, "", "internal", "late")
	if err != nil || second {
		t.Fatalf("second finish = %v, %v", second, err)
	}

	downstream, err := store.EnqueueJob("full_rescan", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := store.SetPluginJobDownstream(created.ID, downstream)
	if err != nil || !attached {
		t.Fatalf("downstream = %v, %v", attached, err)
	}
	attached, err = store.SetPluginJobDownstream(created.ID, downstream)
	if err != nil || attached {
		t.Fatalf("second downstream = %v, %v", attached, err)
	}

	got, err := store.GetPluginJob(created.ID)
	if err != nil || got == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got.Status != PluginJobDone || got.DownstreamJobID != downstream || got.Progress == "" || got.Result == "" {
		t.Fatalf("got = %+v", got)
	}
	if got.Progress != `{"pct":40,"phase":"download","message":"[redacted]"}` {
		t.Fatalf("progress changed after finish: %s", got.Progress)
	}
	if got.Input != created.Input {
		t.Fatalf("input = %s", got.Input)
	}

	listed, err := store.ListPluginJobs(PluginJobDone, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	other, err := store.ListPluginJobs(PluginJobCancelled, 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cancelled list = %+v, %v", other, err)
	}
	missing, err := store.GetPluginJob(created.ID + 1000)
	if err != nil || missing != nil {
		t.Fatalf("missing = %+v, %v", missing, err)
	}
}

func TestPluginJobCancelAndInterrupt(t *testing.T) {
	store, _ := openTestStore(t)
	pending, err := store.CreatePluginJob("example", "library.import_url", `{"url":"https://example.invalid/a"}`)
	if err != nil {
		t.Fatal(err)
	}
	runningJob, err := store.CreatePluginJob("example", "library.import_url", `{"url":"https://example.invalid/b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.MarkPluginJobRunning(runningJob.ID); err != nil || !ok {
		t.Fatalf("mark = %v, %v", ok, err)
	}
	if ok, err := store.FinishPluginJob(pending.ID, PluginJobCancelled, "", "cancelled", "plugin cancelled"); err != nil || !ok {
		t.Fatalf("cancel = %v, %v", ok, err)
	}
	if err := store.InterruptPluginJobs(); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.GetPluginJob(pending.ID)
	if err != nil || cancelled == nil || cancelled.Status != PluginJobCancelled {
		t.Fatalf("pending after interrupt = %+v, %v", cancelled, err)
	}
	interrupted, err := store.GetPluginJob(runningJob.ID)
	if err != nil || interrupted == nil || interrupted.Status != PluginJobError || interrupted.ErrorCode != "internal" {
		t.Fatalf("running after interrupt = %+v, %v", interrupted, err)
	}
	if ok, err := store.FinishPluginJob(runningJob.ID, PluginJobDone, `{"paths":["imports/example/a.flac"]}`, "", ""); err != nil || ok {
		t.Fatalf("finish interrupted = %v, %v", ok, err)
	}
}

func TestFinishPluginImportStoresRescan(t *testing.T) {
	store, _ := openTestStore(t)
	created, err := store.CreatePluginJob("example", "library.import_url", `{"url":"https://example.invalid/a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.MarkPluginJobRunning(created.ID); err != nil || !ok {
		t.Fatalf("mark = %v, %v", ok, err)
	}
	downstream, err := store.EnqueueJob("full_rescan", `{"mixes":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := store.FinishPluginImport(created.ID, `{"paths":["imports/example/track.flac"]}`, downstream)
	if err != nil || !ok {
		t.Fatalf("finish = %v, %v", ok, err)
	}
	got, err := store.GetPluginJob(created.ID)
	if err != nil || got == nil || got.Status != PluginJobDone || got.DownstreamJobID != downstream {
		t.Fatalf("got = %+v, %v", got, err)
	}
	ok, err = store.FinishPluginImport(created.ID, `{"paths":["imports/example/other.flac"]}`, downstream)
	if err != nil || ok {
		t.Fatalf("second finish = %v, %v", ok, err)
	}
}
