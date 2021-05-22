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

// Package python contains Python buildpack library code.
package python

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/buildpacks/pkg/cache"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/env"
	gcp "github.com/GoogleCloudPlatform/buildpacks/pkg/gcpbuildpack"
	"github.com/buildpacks/libcnb"
)

const (
	dateFormat = time.RFC3339Nano
	// expirationTime is an arbitrary amount of time of 1 day to refresh the cache layer.
	expirationTime = time.Duration(time.Hour * 24)

	pythonVersionKey   = "python_version"
	dependencyHashKey  = "dependency_hash"
	expiryTimestampKey = "expiry_timestamp"

	cacheName = "pipcache"

	// RequirementsFilesEnv is an environment variable containg os-path-separator-separated list of paths to pip requirements files.
	// The requirements files are processed from left to right, with requirements from the next overriding any conflicts from the previous.
	RequirementsFilesEnv = "GOOGLE_INTERNAL_REQUIREMENTS_FILES"
)

var (
	// RequirementsProvides denotes that the buildpack provides requirements.txt in the environment.
	RequirementsProvides = []libcnb.BuildPlanProvide{{Name: "requirements.txt"}}
	// RequirementsRequires denotes that the buildpack consumes requirements.txt from the environment.
	RequirementsRequires = []libcnb.BuildPlanRequire{{Name: "requirements.txt"}}
	// RequirementsProvidesPlan is a build plan returned by buildpacks that provide requirements.txt.
	RequirementsProvidesPlan = libcnb.BuildPlan{Provides: RequirementsProvides}
	// RequirementsProvidesRequiresPlan is a build plan returned by buildpacks that consume requirements.txt.
	RequirementsProvidesRequiresPlan = libcnb.BuildPlan{Provides: RequirementsProvides, Requires: RequirementsRequires}
)

// Version returns the installed version of Python.
func Version(ctx *gcp.Context) string {
	result := ctx.Exec([]string{"python3", "--version"})
	return strings.TrimSpace(result.Stdout)
}

// InstallRequirements installs dependencies from the given requirements files in a virtual env.
// It will install the files in order in which they are specified, so that dependencies specified
// in later requirements files can override later ones.
//
// This function is responsible for installing requirements files for all buildpacks that require
// it. The buildpacks used to install requirements into separate layers and add the layer path to
// PYTHONPATH. However, this caused issues with some packages as it would allow users to
// accidentally override some builtin stdlib modules, e.g. typing, enum, etc., and cause both
// build-time and run-time failures.
func InstallRequirements(ctx *gcp.Context, l *libcnb.Layer, reqs ...string) error {
	// Defensive check, this should not happen in practice.
	if len(reqs) == 0 {
		ctx.Debugf("No requirements.txt to install, clearing layer.")
		ctx.ClearLayer(l)
		return nil
	}

	// Check if we can use the cached-layer as is without reinstalling dependencies.
	cached, err := checkCache(ctx, l, cache.WithFiles(reqs...))
	if err != nil {
		return fmt.Errorf("checking cache: %w", err)
	}
	if cached {
		ctx.CacheHit(l.Name)
		return nil
	}
	ctx.CacheMiss(l.Name)

	// The cache layer is used as PIP_CACHE_DIR to keep the cache directory across builds in case
	// we do not get a full cache hit.
	cl := ctx.Layer(cacheName, gcp.CacheLayer)

	// History of the logic below:
	//
	// pip install --target has several subtle issues:
	// We cannot use --upgrade: https://github.com/pypa/pip/issues/8799.
	// We also cannot _not_ use --upgrade, see the requirements_bin_conflict acceptance test.
	//
	// Instead, we use Python per-user site-packages (https://www.python.org/dev/peps/pep-0370/)
	// where we can and virtualenv where we cannot.
	//
	// Each requirements file is installed separately to allow the requirements.txt files
	// to specify conflicting dependencies (e.g. functions-framework pins package A at 1.2.0 but
	// the user's requirements.txt file pins A at 1.4.0. The user should be able to override
	// the functions-framework-pinned package).

	// HACK: For backwards compatibility with Python 3.7 and 3.8 on App Engine and Cloud Functions.
	virtualEnv := requiresVirtualEnv()
	if virtualEnv {
		// --without-pip and --system-site-packages allow us to use `pip` and other packages from the
		// build image and avoid reinstalling them, saving about 10MB.
		// TODO(b/140775593): Use virtualenv pip after FTL is no longer used and remove from build image.
		ctx.Exec([]string{"python3", "-m", "venv", "--without-pip", "--system-site-packages", l.Path})
		// The VIRTUAL_ENV variable is usually set by the virtual environment's activate script.
		l.SharedEnvironment.Override("VIRTUAL_ENV", l.Path)
		// Use the virtual environment python3 for all subsequent commands in this buildpack, for
		// subsequent buildpacks, l.Path/bin will be added by lifecycle.
		ctx.Setenv("PATH", filepath.Join(l.Path, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx.Setenv("VIRTUAL_ENV", l.Path)
	} else {
		l.SharedEnvironment.Default("PYTHONUSERBASE", l.Path)
		ctx.Setenv("PYTHONUSERBASE", l.Path)
	}

	for _, req := range reqs {
		cmd := []string{
			"python3", "-m", "pip", "install",
			"--requirement", req,
			"--upgrade",
			"--upgrade-strategy", "only-if-needed",
			"--no-warn-script-location", // bin is added at run time by lifecycle.
			"--no-warn-conflicts",       // Needed for python37 which allowed users to override dependencies. For newer versions, we do a separate `pip check`.
			"--force-reinstall",         // Some dependencies may be in the build image but not run image. Later requirements.txt should override earlier.
			"--no-compile",              // Prevent default timestamp-based bytecode compilation. Deterministic pycs are generated in a second step below.
		}
		if !virtualEnv {
			cmd = append(cmd, "--user") // Install into user site-packages directory.
		}
		ctx.Exec(cmd,
			gcp.WithEnv("PIP_CACHE_DIR="+cl.Path, "PIP_DISABLE_PIP_VERSION_CHECK=1"),
			gcp.WithUserAttribution)
	}

	// Generate deterministic hash-based pycs (https://www.python.org/dev/peps/pep-0552/).
	// Use the unchecked version to skip hash validation at run time (for faster startup).
	result, cerr := ctx.ExecWithErr([]string{
		"python3", "-m", "compileall",
		"--invalidation-mode", "unchecked-hash",
		"-qq", // Do not print any message (matches `pip install` behavior).
		l.Path,
	},
		gcp.WithUserAttribution)
	if cerr != nil {
		if result != nil {
			if result.ExitCode == 1 {
				// Ignore file compilation errors (matches `pip install` behavior).
				return nil
			}
			return fmt.Errorf("compileall: %s", result.Combined)
		}
		return fmt.Errorf("compileall: %v", cerr)
	}

	return nil
}

// checkCache checks whether cached dependencies exist, match, and have not expired.
func checkCache(ctx *gcp.Context, l *libcnb.Layer, opts ...cache.Option) (bool, error) {
	currentPythonVersion := Version(ctx)
	opts = append(opts, cache.WithStrings(currentPythonVersion))
	currentDependencyHash, err := cache.Hash(ctx, opts...)
	if err != nil {
		return false, fmt.Errorf("computing dependency hash: %v", err)
	}

	metaDependencyHash := ctx.GetMetadata(l, dependencyHashKey)
	// Check cache expiration to pick up new versions of dependencies that are not pinned.
	expired := cacheExpired(ctx, l)

	// Perform install, skipping if the dependency hash matches existing metadata.
	ctx.Debugf("Current dependency hash: %q", currentDependencyHash)
	ctx.Debugf("  Cache dependency hash: %q", metaDependencyHash)
	if currentDependencyHash == metaDependencyHash && !expired {
		ctx.Logf("Dependencies cache hit, skipping installation.")
		return true, nil
	}

	if metaDependencyHash == "" {
		ctx.Debugf("No metadata found from a previous build, skipping cache.")
	}

	ctx.ClearLayer(l)

	ctx.Logf("Installing application dependencies.")
	// Update the layer metadata.
	ctx.SetMetadata(l, dependencyHashKey, currentDependencyHash)
	ctx.SetMetadata(l, pythonVersionKey, currentPythonVersion)
	ctx.SetMetadata(l, expiryTimestampKey, time.Now().Add(expirationTime).Format(dateFormat))

	return false, nil
}

// cacheExpired returns true when the cache is past expiration.
func cacheExpired(ctx *gcp.Context, l *libcnb.Layer) bool {
	t := time.Now()
	expiry := ctx.GetMetadata(l, expiryTimestampKey)
	if expiry != "" {
		var err error
		t, err = time.Parse(dateFormat, expiry)
		if err != nil {
			ctx.Debugf("Could not parse expiration date %q, assuming now: %v", expiry, err)
		}
	}
	return !t.After(time.Now())
}

// requiresVirtualEnv returns true for runtimes that require a virtual environment to be created before pip install.
// We cannot use Python per-user site-packages (https://www.python.org/dev/peps/pep-0370/),
// because Python 3.7 and 3.8 on App Engine and Cloud Functions have a virtualenv set up
// that disables user site-packages. The base images include a virtual environment pointing to
// a directory that is not writeable in the buildpacks world (/env). In order to keep
// compatiblity with base image updates, we replace the virtual environment with a writeable one.
func requiresVirtualEnv() bool {
	runtime := os.Getenv(env.Runtime)
	return runtime == "python37" || runtime == "python38"
}
