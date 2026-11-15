// Package plugins discovers local protocol v1 executables and runs one task
// per process.
//
// Manifests live in MUSIK_PLUGIN_DIRS or $XDG_DATA_HOME/musik/plugins. The
// player executes a manifest path directly, with no shell, and speaks JSON
// Lines on stdin and stdout. Cancellation and timeouts send SIGTERM to the
// plugin process group, then SIGKILL if it stays alive. Request URLs, search
// queries, and player secrets stay out of the plugin environment, argv,
// public errors, and logs.
package plugins
