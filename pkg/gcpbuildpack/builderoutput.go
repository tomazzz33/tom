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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	errorIDLength         = 8
	builderOutputEnv      = "BUILDER_OUTPUT"
	builderOutputFilename = "output"
)

var (
	maxMessageBytes = 3000
)

// ErrorID is a short error code passed to the user for supportability.
type ErrorID string

type builderOutput struct {
	Error Error         `json:"error"`
	Stats []builderStat `json:"stats"`
}

// Error is a gcpbuildpack structured error.
type Error struct {
	BuildpackID      string  `json:"buildpackId"`
	BuildpackVersion string  `json:"buildpackVersion"`
	Type             Status  `json:"errorType"`
	Status           Status  `json:"canonicalCode"`
	ID               ErrorID `json:"errorId"`
	Message          string  `json:"errorMessage"`
}

type builderStat struct {
	BuildpackID      string `json:"buildpackId"`
	BuildpackVersion string `json:"buildpackVersion"`
	DurationMs       int64  `json:"totalDurationMs"`
	UserDurationMs   int64  `json:"userDurationMs"`
}

func (e *Error) Error() string {
	if e.ID == "" {
		return e.Message
	}
	return fmt.Sprintf("%s [id:%s]", e.Message, e.ID)
}

// Errorf constructs an Error.
func Errorf(status Status, format string, args ...interface{}) *Error {
	msg := fmt.Sprintf(format, args...)
	return &Error{
		Type:    status,
		Status:  status,
		ID:      generateErrorID(msg),
		Message: msg,
	}
}

// InternalErrorf constructs an Error with status StatusInternal (Google-attributed SLO).
func InternalErrorf(format string, args ...interface{}) *Error {
	return Errorf(StatusInternal, format, args...)
}

// UserErrorf constructs an Error with status StatusUnknown (user-attributed SLO).
func UserErrorf(format string, args ...interface{}) *Error {
	return Errorf(StatusUnknown, format, args...)
}

// saveErrorOutput saves to the builder output file, if appropriate.
func (ctx *Context) saveErrorOutput(be *Error) {
	outputDir := os.Getenv(builderOutputEnv)
	if outputDir == "" {
		return
	}

	if len(be.Message) > maxMessageBytes {
		be.Message = keepTail(be.Message)
	}

	be.BuildpackID, be.BuildpackVersion = ctx.BuildpackID(), ctx.BuildpackVersion()
	bo := builderOutput{Error: *be}
	data, err := json.Marshal(&bo)
	if err != nil {
		ctx.Warnf("Failed to marshal, skipping structured error output: %v", err)
		return
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		ctx.Warnf("Failed to create dir %s, skipping structured error output: %v", outputDir, err)
		return
	}

	// /bin/detect steps run in parallel, so they might compete over the output file. To eliminate
	// this competition, write to temp file, then `mv -f` to final location (last one in wins).
	tname := filepath.Join(outputDir, fmt.Sprintf("%s-%d", builderOutputFilename, rand.Int()))
	if err := ioutil.WriteFile(tname, data, 0644); err != nil {
		ctx.Warnf("Failed to write %s, skipping structured error output: %v", tname, err)
		return
	}
	fname := filepath.Join(outputDir, builderOutputFilename)
	if _, err := ctx.ExecWithErr([]string{"mv", "-f", tname, fname}); err != nil {
		ctx.Warnf("Failed to move %s to %s, skipping structured error output: %v", tname, fname, err)
		return
	}
	return
}

// ErrorSummaryProducer is responsible to produce summary error information.
// The function must return a *gcpbuildpack.Error. The overall BUILDER_OUTPUT
// file has a maximum size, so the message must be at most maxMessageBytes; this formatter
// should attempt to shorten the message to something user actionable if the message
// is too long. There are helper functions that can be used as the formatter, like
// ErrorKeepStdoutTail(), ErrorKeepStderrTail(), etc.
type ErrorSummaryProducer func(result *ExecResult) *Error

// UserErrorKeepStdoutTail returns a user error that keeps stdout tail.
func UserErrorKeepStdoutTail(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepTail(result.Stdout))
}

// UserErrorKeepStderrTail returns a user error that keeps stderr tail.
func UserErrorKeepStderrTail(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepTail(result.Stderr))
}

// UserErrorKeepCombinedTail returns a user error that keeps combined stdout/stderr tail.
func UserErrorKeepCombinedTail(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepTail(result.Combined))
}

// UserErrorKeepStdoutHead returns a user error that keeps stdout head.
func UserErrorKeepStdoutHead(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepHead(result.Stdout))
}

// UserErrorKeepStderrHead returns a user error that keeps stderr head.
func UserErrorKeepStderrHead(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepHead(result.Stderr))
}

// UserErrorKeepCombinedHead returns a user error that keeps combined stdout/stderr head.
func UserErrorKeepCombinedHead(result *ExecResult) *Error {
	return Errorf(StatusUnknown, keepHead(result.Combined))
}

func keepTail(message string) string {
	message = strings.TrimSpace(message)

	if len(message) <= maxMessageBytes {
		return message
	}

	return "..." + message[len(message)-maxMessageBytes+3:]
}

func keepHead(message string) string {
	message = strings.TrimSpace(message)

	if len(message) <= maxMessageBytes {
		return message
	}

	return message[:maxMessageBytes-3] + "..."
}

// generateErrorID creates a short hash from the provided parts.
func generateErrorID(parts ...string) ErrorID {
	h := sha256.New()
	for _, p := range parts {
		io.WriteString(h, p)
	}
	result := fmt.Sprintf("%x", h.Sum(nil))

	// Since this is only a reporting aid for support, we truncate the hash to make it more human friendly.
	return ErrorID(strings.ToLower(result[:errorIDLength]))
}

func (ctx *Context) saveSuccessOutput(duration time.Duration) {
	outputDir := os.Getenv(builderOutputEnv)
	if outputDir == "" {
		return
	}

	var bo builderOutput
	fname := filepath.Join(outputDir, builderOutputFilename)

	if ctx.FileExists(fname) {
		content, err := ioutil.ReadFile(fname)
		if err != nil {
			ctx.Warnf("Failed to read %s, skipping statistics: %v", fname, err)
			return
		}
		if err := json.Unmarshal(content, &bo); err != nil {
			ctx.Warnf("Failed to unmarshal %s, skipping statistics: %v", fname, err)
			return
		}
	}

	bo.Stats = append(bo.Stats, builderStat{
		BuildpackID:      ctx.BuildpackID(),
		BuildpackVersion: ctx.BuildpackVersion(),
		DurationMs:       duration.Milliseconds(),
		UserDurationMs:   ctx.stats.user.Milliseconds(),
	})

	content, err := json.Marshal(&bo)
	if err != nil {
		ctx.Warnf("Failed to marshal stats, skipping statistics: %v", err)
		return
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		ctx.Warnf("Failed to create dir %s, skipping statistics: %v", outputDir, err)
		return
	}
	if err := ioutil.WriteFile(fname, content, 0644); err != nil {
		ctx.Warnf("Failed to write %s, skipping statistics: %v", fname, err)
		return
	}
}
