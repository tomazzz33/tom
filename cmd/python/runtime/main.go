// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Implements python/runtime buildpack.
// The runtime buildpack installs the Python runtime.
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/buildpacks/pkg/env"
	gcp "github.com/GoogleCloudPlatform/buildpacks/pkg/gcpbuildpack"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/runtime"
	"github.com/buildpacks/libcnb"
)

const (
	pythonLayer = "python"
	pythonURL   = "https://storage.googleapis.com/gcp-buildpacks/python/python-%s.tar.gz"
	// TODO(b/148375706): Add mapping for stable/beta versions.
	versionURL  = "https://storage.googleapis.com/gcp-buildpacks/python/latest.version"
	versionFile = ".python-version"
	versionKey  = "version"
)

func main() {
	gcp.Main(detectFn, buildFn)
}

func detectFn(ctx *gcp.Context) (gcp.DetectResult, error) {
	if result := runtime.CheckOverride(ctx, "python"); result != nil {
		return result, nil
	}

	if !ctx.HasAtLeastOne("*.py") {
		return gcp.OptOut("no .py files found"), nil
	}
	return gcp.OptIn("found .py files"), nil
}

func buildFn(ctx *gcp.Context) error {
	version, err := runtimeVersion(ctx)
	if err != nil {
		return fmt.Errorf("determining runtime version: %w", err)
	}

	l := ctx.Layer(pythonLayer, gcp.BuildLayer, gcp.CacheLayer, gcp.LaunchLayer)

	// Check the metadata in the cache layer to determine if we need to proceed.
	metaVersion := ctx.GetMetadata(l, versionKey)
	if version == metaVersion {
		ctx.CacheHit(pythonLayer)
		return nil
	}
	ctx.CacheMiss(pythonLayer)
	ctx.ClearLayer(l)

	archiveURL := fmt.Sprintf(pythonURL, version)
	if code := ctx.HTTPStatus(archiveURL); code != http.StatusOK {
		return gcp.UserErrorf("Runtime version %s does not exist at %s (status %d). You can specify the version with %s.", version, archiveURL, code, env.RuntimeVersion)
	}

	ctx.Logf("Installing Python v%s", version)
	command := fmt.Sprintf("curl --fail --show-error --silent --location --retry 3 %s | tar xz --directory %s", archiveURL, l.Path)
	ctx.Exec([]string{"bash", "-c", command})

	ctx.Logf("Upgrading pip to the latest version and installing build tools")
	path := filepath.Join(l.Path, "bin/python3")
	ctx.Exec([]string{path, "-m", "pip", "install", "--upgrade", "pip", "setuptools", "wheel"}, gcp.WithUserAttribution)

	// Force stdout/stderr streams to be unbuffered so that log messages appear immediately in the logs.
	l.LaunchEnvironment.Default("PYTHONUNBUFFERED", "TRUE")

	ctx.SetMetadata(l, versionKey, version)
	ctx.AddBuildpackPlanEntry(libcnb.BuildpackPlanEntry{
		Name:     pythonLayer,
		Metadata: map[string]interface{}{"version": version},
	})

	return nil
}

func runtimeVersion(ctx *gcp.Context) (string, error) {
	if v := os.Getenv(env.RuntimeVersion); v != "" {
		ctx.Logf("Using runtime version from %s: %s", env.RuntimeVersion, v)
		return v, nil
	}
	if ctx.FileExists(versionFile) {
		raw := ctx.ReadFile(versionFile)
		v := strings.TrimSpace(string(raw))
		if v != "" {
			ctx.Logf("Using runtime version from %s: %s", versionFile, v)
			return v, nil
		}
		return "", gcp.UserErrorf("%s exists but does not specify a version", versionFile)
	}
	// Intentionally no user-attributed becase the URL is provided by Google.
	v := ctx.Exec([]string{"curl", "--fail", "--show-error", "--silent", "--location", versionURL}).Stdout
	ctx.Logf("Using latest runtime version: %s", v)
	return v, nil
}
