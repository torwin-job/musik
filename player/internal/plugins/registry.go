package plugins

import (
	"context"
	"errors"
)

// Plugin is one executable the host can run.
type Plugin struct {
	ID           string
	Executable   string
	Capabilities []string
	Name         string
	Version      string
	Description  string
	Manifest     Manifest
	Describe     Describe
}

// Rejection explains why a manifest or describe result was left inactive.
type Rejection struct {
	Source string
	ID     string
	Reason string
}

// Registry is the set of plugins discovery accepted. It is immutable.
type Registry struct {
	plugins  []Plugin
	rejected []Rejection
}

// List returns the active plugins in discovery order.
func (r *Registry) List() []Plugin {
	if r == nil {
		return nil
	}
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Get returns the active plugin with id.
func (r *Registry) Get(id string) (Plugin, bool) {
	if r == nil {
		return Plugin{}, false
	}
	for _, plugin := range r.plugins {
		if plugin.ID == id {
			return plugin, true
		}
	}
	return Plugin{}, false
}

// ByCapability returns active plugins that advertise a usable capability.
func (r *Registry) ByCapability(name string) []Plugin {
	if r == nil {
		return nil
	}
	var out []Plugin
	for _, plugin := range r.plugins {
		if hasCapability(plugin, name) {
			out = append(out, plugin)
		}
	}
	return out
}

// Rejections returns manifests and describe results left inactive.
func (r *Registry) Rejections() []Rejection {
	if r == nil {
		return nil
	}
	out := make([]Rejection, len(r.rejected))
	copy(out, r.rejected)
	return out
}

// Discover scans dirs for manifests, describes each unique executable, and
// keeps plugins whose usable capabilities include library.import_url.
// A missing directory is skipped. An id that appears on more than one valid
// manifest is left inactive.
func (r *Runner) Discover(ctx context.Context, dirs []string) (*Registry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, rejected, err := ManifestFiles(dirs)
	if err != nil {
		return nil, err
	}
	var loaded []Manifest
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		manifest, err := LoadManifest(file)
		if err != nil {
			rejected = append(rejected, Rejection{Source: file, ID: manifest.ID, Reason: err.Error()})
			continue
		}
		loaded = append(loaded, manifest)
	}
	conflicts := map[string]struct{}{}
	for _, id := range ConflictingIDs(loaded) {
		conflicts[id] = struct{}{}
	}
	registry := &Registry{}
	var pending []Manifest
	for _, manifest := range loaded {
		if _, dup := conflicts[manifest.ID]; dup {
			rejected = append(rejected, Rejection{
				Source: manifest.Source,
				ID:     manifest.ID,
				Reason: "duplicate plugin id",
			})
			continue
		}
		pending = append(pending, manifest)
	}
	for _, manifest := range pending {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		described, err := r.Describe(ctx, manifest.ResolvedExecutable)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			rejected = append(rejected, Rejection{
				Source: manifest.Source,
				ID:     manifest.ID,
				Reason: describeRejection(err),
			})
			continue
		}
		usable, err := CompatibleCapabilities(manifest, described)
		if err != nil {
			rejected = append(rejected, Rejection{
				Source: manifest.Source,
				ID:     manifest.ID,
				Reason: err.Error(),
			})
			continue
		}
		registry.plugins = append(registry.plugins, pluginFrom(manifest, described, usable))
	}
	registry.rejected = rejected
	return registry, nil
}

func pluginFrom(manifest Manifest, described Describe, usable []string) Plugin {
	name := manifest.Name
	version := manifest.Version
	description := manifest.Description
	if described.Name != "" {
		name = described.Name
	}
	if described.Version != "" {
		version = described.Version
	}
	if described.Description != "" {
		description = described.Description
	}
	return Plugin{
		ID:           manifest.ID,
		Executable:   manifest.ResolvedExecutable,
		Capabilities: usable,
		Name:         name,
		Version:      version,
		Description:  description,
		Manifest:     manifest,
		Describe:     described,
	}
}

func describeRejection(err error) string {
	var failure *Failure
	if errors.As(err, &failure) && failure.Message == "plugin timed out" {
		return "describe timed out"
	}
	return "describe failed"
}

func hasCapability(plugin Plugin, name string) bool {
	for _, capability := range plugin.Capabilities {
		if capability == name {
			return true
		}
	}
	return false
}
