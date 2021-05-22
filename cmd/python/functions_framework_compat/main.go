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

// Implements python/functions_framework_compat buildpack.
// The functions_framework buildpack installs dependencies that were included with the python37 runtime.
package main

import (
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/buildpacks/pkg/env"
	gcp "github.com/GoogleCloudPlatform/buildpacks/pkg/gcpbuildpack"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/python"
)

const (
	layerName = "functions-framework-compat"
)

func main() {
	gcp.Main(detectFn, buildFn)
}

func detectFn(ctx *gcp.Context) (gcp.DetectResult, error) {
	if _, ok := os.LookupEnv(env.FunctionTarget); ok {
		return gcp.OptInEnvSet(env.FunctionTarget, gcp.WithBuildPlans(python.RequirementsProvidesPlan)), nil
	}
	return gcp.OptOutEnvNotSet(env.FunctionTarget), nil
}

func buildFn(ctx *gcp.Context) error {
	l := ctx.Layer(layerName, gcp.LaunchLayer, gcp.BuildLayer)

	// The pip install is performed by the pip buildpack; see python.InstallRequirements.
	ctx.Debugf("Adding functions-framework requirements.txt to the list of requirements files to install.")
	r := filepath.Join(ctx.BuildpackRoot(), "converter", "requirements.txt")
	l.BuildEnvironment.Append(python.RequirementsFilesEnv, string(os.PathListSeparator)+r)

	// Set additional Python 3.7 env var for backwards compatibility.
	l.LaunchEnvironment.Default("ENTRY_POINT", os.Getenv(env.FunctionTarget))

	return nil
}
