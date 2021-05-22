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

package gcpbuildpack

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/buildpacks/libcnb"
)

func TestSaveErrorOutput(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "save-error-output-")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}

	os.Setenv("BUILDER_OUTPUT", tempDir)
	defer func() {
		os.Unsetenv("BUILDER_OUTPUT")
	}()

	oldMax := maxMessageBytes
	maxMessageBytes = 8
	defer func() {
		maxMessageBytes = oldMax
	}()
	ctx := NewContext(libcnb.BuildpackInfo{ID: "id", Version: "version", Name: "name"})
	msg := "This is a long message that will be truncated."

	ctx.saveErrorOutput(Errorf(StatusInternal, msg))

	data, err := ioutil.ReadFile(filepath.Join(tempDir, "output"))
	if err != nil {
		t.Fatalf("failed to read expected file $BUILDER_OUTPUT/output: %v", err)
	}
	var got builderOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to unmarshal json: %v", err)
	}

	want := builderOutput{
		Error: Error{
			BuildpackID:      "id",
			BuildpackVersion: "version",
			Type:             StatusInternal,
			Status:           StatusInternal,
			ID:               generateErrorID(msg),
			Message:          "...ated.",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected output does not match\ngot:\n%#v\nwant:\n%#v", got, want)
	}
}

func TestMessageProducers(t *testing.T) {
	testCases := []struct {
		name     string
		producer MessageProducer
		stdout   string
		stderr   string
		combined string
		want     string
	}{
		{
			name:     "KeepStdoutTail",
			producer: KeepStdoutTail,
			stdout:   "123456789stdout",
			want:     "...stdout",
		},
		{
			name:     "KeepStderrTail",
			producer: KeepStderrTail,
			stderr:   "123456789stderr",
			want:     "...stderr",
		},
		{
			name:     "KeepCombinedTail",
			producer: KeepCombinedTail,
			combined: "123456789combined",
			want:     "...mbined",
		},
		{
			name:     "KeepStdoutHead",
			producer: KeepStdoutHead,
			stdout:   "stdout123456789",
			want:     "stdout...",
		},
		{
			name:     "KeepStderrHead",
			producer: KeepStderrHead,
			stderr:   "stderr123456789",
			want:     "stderr...",
		},
		{
			name:     "KeepCombinedHead",
			producer: KeepCombinedHead,
			combined: "combined123456789",
			want:     "combin...",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			oldMax := maxMessageBytes
			maxMessageBytes = 9
			defer func() {
				maxMessageBytes = oldMax
			}()

			got := tc.producer(&ExecResult{Stdout: tc.stdout, Stderr: tc.stderr, Combined: tc.combined})

			if got != tc.want {
				t.Errorf("message got %q want %q", got, tc.want)
			}
		})
	}
}

func TestKeepTail(t *testing.T) {
	testCases := []struct {
		name            string
		message         string
		want            string
		maxMessageBytes int
	}{
		{
			name:            "empty message",
			message:         "",
			want:            "",
			maxMessageBytes: 8,
		},
		{
			name:            "short message",
			message:         "123",
			want:            "123",
			maxMessageBytes: 8,
		},
		{
			name:            "long message",
			message:         "12345678901234567890",
			want:            "...67890",
			maxMessageBytes: 8,
		},
		{
			name:            "boundary message 1",
			message:         "12345678",
			want:            "12345678",
			maxMessageBytes: 8,
		},
		{
			name:            "boundary message 2",
			message:         "123456789",
			want:            "...56789",
			maxMessageBytes: 8,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.maxMessageBytes != 0 {
				oldMax := maxMessageBytes
				maxMessageBytes = tc.maxMessageBytes
				defer func() {
					maxMessageBytes = oldMax
				}()
			}
			got := keepTail(tc.message)
			if got != tc.want {
				t.Errorf("keepTail() got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestKeepHead(t *testing.T) {
	testCases := []struct {
		name            string
		message         string
		want            string
		maxMessageBytes int
	}{
		{
			name:            "empty message",
			message:         "",
			want:            "",
			maxMessageBytes: 8,
		},
		{
			name:            "short message",
			message:         "123",
			want:            "123",
			maxMessageBytes: 8,
		},
		{
			name:            "long message",
			message:         "12345678901234567890",
			want:            "12345...",
			maxMessageBytes: 8,
		},
		{
			name:            "boundary message 1",
			message:         "12345678",
			want:            "12345678",
			maxMessageBytes: 8,
		},
		{
			name:            "boundary message 2",
			message:         "123456789",
			want:            "12345...",
			maxMessageBytes: 8,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.maxMessageBytes != 0 {
				oldMax := maxMessageBytes
				maxMessageBytes = tc.maxMessageBytes
				defer func() {
					maxMessageBytes = oldMax
				}()
			}
			got := keepHead(tc.message)
			if got != tc.want {
				t.Errorf("keepHead() got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestGenerateErrorId(t *testing.T) {
	result1 := generateErrorID("abc", "def")
	if len(result1) != errorIDLength {
		t.Fatalf("len errorId got %d, want %d", len(result1), errorIDLength)
	}

	result2 := generateErrorID("abc")
	if result2 == result1 {
		t.Errorf("error IDs are not unique to different inputs")
	}
}

func TestSaveBuilderSuccessOutput(t *testing.T) {
	dur := 30 * time.Second
	userDur := 5 * time.Second
	buildpackID, buildpackVersion := "my-id", "my-version"

	testCases := []struct {
		name     string
		initial  *builderOutput
		warnings []string
		want     builderOutput
	}{
		{
			name: "no file",
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
			},
		},
		{
			name:     "no file warnings",
			warnings: []string{"Test warning about a conflicting file."},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{"Test warning about a conflicting file."},
			},
		},
		{
			name: "existing file",
			initial: &builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
				},
			},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
			},
		},
		{
			name: "existing file new warnings",
			initial: &builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
				},
			},
			warnings: []string{"Test warning about a conflicting file."},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{"Test warning about a conflicting file."},
			},
		},
		{
			name: "existing file existing warnings",
			initial: &builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
				},
				Warnings: []string{"Test warning from a previous buildpack."},
			},
			warnings: []string{"Test warning about a conflicting file."},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: "bp1", BuildpackVersion: "v1", DurationMs: 1000, UserDurationMs: 100},
					{BuildpackID: "bp2", BuildpackVersion: "v2", DurationMs: 2000, UserDurationMs: 200},
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{
					"Test warning from a previous buildpack.",
					"Test warning about a conflicting file.",
				},
			},
		},
		{
			name: "warnings trim last",
			warnings: []string{
				"Test warning about a conflicting file.",
				strings.Repeat("x", maxMessageBytes),
			},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{
					"Test warning about a conflicting file.",
					strings.Repeat("x", 2709) + "...",
				},
			},
		},
		{
			name: "warnings trim last short",
			warnings: []string{"Test warning about a conflicting file.",
				strings.Repeat("x", 2709-4), // Four bytes shorter than the maximum which should leave exactly one character for the second warning.
				strings.Repeat("y", maxMessageBytes),
			},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{
					"Test warning about a conflicting file.",
					strings.Repeat("x", 2705),
					"y...",
				},
			},
		},
		{
			name: "warnings drop last short",
			warnings: []string{"Test warning about a conflicting file.",
				strings.Repeat("x", 2709-3), // Three bytes shorter than the maximum, which would leave 3 characters for the last warning so we drop it.
				strings.Repeat("y", maxMessageBytes),
			},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{
					"Test warning about a conflicting file.",
					strings.Repeat("x", 2706),
				},
			},
		},
		{
			name: "warnings drop last and trim",
			warnings: []string{"Test warning about a conflicting file.",
				strings.Repeat("x", maxMessageBytes),
				strings.Repeat("y", maxMessageBytes),
			},
			want: builderOutput{
				Stats: []builderStat{
					{BuildpackID: buildpackID, BuildpackVersion: buildpackVersion, DurationMs: dur.Milliseconds(), UserDurationMs: userDur.Milliseconds()},
				},
				Warnings: []string{
					"Test warning about a conflicting file.",
					strings.Repeat("x", 2709) + "...",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir, err := ioutil.TempDir("", "save-success-output-")
			if err != nil {
				t.Fatalf("creating temp dir: %v", err)
			}

			os.Setenv("BUILDER_OUTPUT", tempDir)
			defer func() {
				os.Unsetenv("BUILDER_OUTPUT")
			}()

			fname := filepath.Join(tempDir, builderOutputFilename)
			if tc.initial != nil {
				content, err := json.Marshal(tc.initial)
				if err != nil {
					t.Fatalf("Failed to marshal stats: %v", err)
				}
				if err := ioutil.WriteFile(fname, content, 0644); err != nil {
					t.Fatalf("Failed to write %s: %v", fname, err)
				}
			}
			ctx := NewContext(libcnb.BuildpackInfo{ID: buildpackID, Version: buildpackVersion, Name: "name"})
			ctx.stats.user = userDur
			ctx.warnings = tc.warnings

			ctx.saveSuccessOutput(dur)

			var got builderOutput
			content, err := ioutil.ReadFile(fname)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", fname, err)
			}
			if err := json.Unmarshal(content, &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Expected stats do not match got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMarshalJSON(t *testing.T) {
	b := builderOutput{Error: Error{Status: StatusInternal}}

	s, err := json.Marshal(b)

	if err != nil {
		t.Fatalf("Failed to marshal %v: %v", b, err)
	}
	if !strings.Contains(string(s), "INTERNAL") {
		t.Errorf("Expected string 'INTERNAL' not found in %s", s)
	}
}
