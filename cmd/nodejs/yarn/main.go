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

// Implements /bin/build for nodejs/dependencies buildpack.
package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/GoogleCloudPlatform/buildpacks/pkg/devmode"
	gcp "github.com/GoogleCloudPlatform/buildpacks/pkg/gcpbuildpack"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/nodejs"
	"github.com/buildpack/libbuildpack/buildpackplan"
	"github.com/buildpack/libbuildpack/layers"
)

const (
	cacheTag = "prod dependencies"
	yarnURL  = "https://github.com/yarnpkg/yarn/releases/download/v%[1]s/yarn-v%[1]s.tar.gz"
)

// metadata represents metadata stored for a yarn layer.
type metadata struct {
	Version string `toml:"version"`
}

func main() {
	gcp.Main(detectFn, buildFn)
}

func detectFn(ctx *gcp.Context) error {
	if !ctx.FileExists(nodejs.YarnLock) {
		ctx.OptOut("yarn.lock not found.")
	}
	if !ctx.FileExists("package.json") {
		ctx.OptOut("package.json not found.")
	}
	return nil
}

func buildFn(ctx *gcp.Context) error {
	if err := installYarn(ctx); err != nil {
		return fmt.Errorf("installing Yarn: %w", err)
	}

	l := ctx.Layer("yarn")
	nm := path.Join(l.Root, "node_modules")
	ctx.RemoveAll("node_modules")

	cached, meta, err := nodejs.CheckCache(ctx, l, nodejs.YarnLock)
	if err != nil {
		return fmt.Errorf("checking cache: %w", err)
	}

	if cached {
		ctx.CacheHit(cacheTag)
		// Restore cached node_modules.
		ctx.Symlink(nm, "node_modules")
	} else {
		ctx.CacheMiss(cacheTag)
		ctx.MkdirAll(nm, 0755)
		ctx.Symlink(nm, "node_modules")
		// Install dependencies in symlinked node_modules.
		ctx.ExecUser([]string{"yarn", "install", "--frozen-lockfile", "--production"})
	}

	ctx.PrependPathSharedEnv(l, "PATH", path.Join(nm, ".bin"))
	ctx.DefaultLaunchEnv(l, "NODE_ENV", "production")
	ctx.WriteMetadata(l, &meta, layers.Build, layers.Cache, layers.Launch)

	// Configure the entrypoint for production.
	cmd := []string{"yarn", "run", "start"}

	if !devmode.Enabled(ctx) {
		ctx.AddWebProcess(cmd)
		return nil
	}

	// Configure the entrypoint for dev mode.
	devmode.AddFileWatcherProcess(ctx, devmode.Config{
		Cmd:  cmd,
		Ext:  devmode.NodeWatchedExtensions,
		Sync: devmode.NodeSyncRules(ctx.ApplicationRoot()),
	})

	return nil
}

func installYarn(ctx *gcp.Context) error {
	// Skip installation if yarn is already installed.
	if result := ctx.Exec([]string{"bash", "-c", "command -v yarn || true"}); result.Stdout != "" {
		ctx.Debugf("Yarn is already installed, skipping installation.")
		return nil
	}

	// Use semver.io to determine the latest available version of Yarn.
	ctx.Logf("Finding latest stable version of Yarn.")
	result := ctx.Exec([]string{"curl", "--silent", "--get", "http://semver.io/yarn/stable"})
	version := result.Stdout
	ctx.Logf("The latest stable version of Yarn is v%s", version)

	yarnLayer := "yarn_install"
	yrl := ctx.Layer(yarnLayer)

	// Check the metadata in the cache layer to determine if we need to proceed.
	var meta metadata
	ctx.ReadMetadata(yrl, &meta)
	if version == meta.Version {
		ctx.CacheHit(yarnLayer)
		ctx.Logf("Yarn cache hit, skipping installation.")
		return nil
	}
	ctx.CacheMiss(yarnLayer)
	ctx.ClearLayer(yrl)

	// Download and install yarn in layer.
	ctx.Logf("Installing Yarn v%s", version)
	archiveURL := fmt.Sprintf(yarnURL, version)
	command := fmt.Sprintf("curl --fail --show-error --silent --location %s | tar xz --directory=%s --strip-components=1", archiveURL, yrl.Root)
	ctx.Exec([]string{"bash", "-c", command})

	// Store layer flags and metadata.
	meta.Version = version
	ctx.WriteMetadata(yrl, meta, layers.Build, layers.Cache, layers.Launch)
	ctx.Setenv("PATH", filepath.Join(yrl.Root, "bin")+":"+os.Getenv("PATH"))

	ctx.AddBuildpackPlan(buildpackplan.Plan{
		Name:    yarnLayer,
		Version: version,
	})
	return nil
}
