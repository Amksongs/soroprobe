package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi/abitest"
	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// fakeBuilder stands in for git and docker.
type fakeBuilder struct {
	result *buildResult
	err    error
	got    []buildRequest
}

func (f *fakeBuilder) Build(_ context.Context, req buildRequest) (*buildResult, error) {
	f.got = append(f.got, req)
	return f.result, f.err
}

func runVerify(t *testing.T, fake *stellartest.Fake, b builder, args ...string) (string, error) {
	t.Helper()
	t.Chdir(t.TempDir())

	cmd, opts := newRootCmd()
	opts.builder = b
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: cfg.SourceAccount,
			Thresholds:    health.DefaultThresholds,
		})
		return p, func() {}, err
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"verify"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

const (
	pinnedImage = "ghcr.io/example/builder@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	repoURL     = "https://github.com/example/token"
)

// deployed puts a contract with build metadata on the fake and returns its
// Wasm hash.
func deployed(t *testing.T, fake *stellartest.Fake, meta ...abitest.Meta) string {
	t.Helper()
	wasm := abitest.WasmWithMeta(t, []xdr.ScSpecEntry{abitest.Fn("get", nil)}, meta)
	return fake.DeployWasm(t, stellartest.UndeployedContract, wasm)
}

func fullMeta() []abitest.Meta {
	return []abitest.Meta{
		{Key: "source_repo", Val: repoURL},
		{Key: "commit_hash", Val: "abc123"},
		{Key: "bldimg", Val: pinnedImage},
	}
}

func TestVerifyMatchesWhenABuiltArtifactHasTheLiveHash(t *testing.T) {
	fake := stellartest.NewFake(t)
	hash := deployed(t, fake, fullMeta()...)
	b := &fakeBuilder{result: &buildResult{
		Toolchain: toolchain{Rustc: "rustc 1.91.0", Stellar: "stellar 25.0.0"},
		Artifacts: []builtArtifact{
			{Path: "target/wasm32v1-none/release/other.wasm", SHA256: "00"},
			{Path: "target/wasm32v1-none/release/token.wasm", SHA256: hash},
		},
	}}

	out, err := runVerify(t, fake, b, stellartest.UndeployedContract)
	require.NoError(t, err)

	// Defaults came from the contract's own metadata.
	require.Len(t, b.got, 1)
	assert.Equal(t, buildRequest{Source: repoURL, Rev: "abc123", Image: pinnedImage}, b.got[0])

	assert.Contains(t, out, "VERIFIED")
	assert.Contains(t, out, "pinned by digest")
	assert.Contains(t, out, "rustc 1.91.0")
	assert.Contains(t, out, "stellar 25.0.0")
	assert.Contains(t, out, "MATCH")
	assert.Contains(t, out, "MISMATCH")
}

func TestVerifyExitsOneWhenNothingMatches(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake, fullMeta()...)
	b := &fakeBuilder{result: &buildResult{Artifacts: []builtArtifact{{Path: "a.wasm", SHA256: "ff"}}}}

	out, err := runVerify(t, fake, b, stellartest.UndeployedContract)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "NOT VERIFIED")

	b = &fakeBuilder{result: &buildResult{}}
	out, err = runVerify(t, fake, b, stellartest.UndeployedContract)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "no .wasm files")
}

func TestVerifyFlagsOverrideMetadata(t *testing.T) {
	fake := stellartest.NewFake(t)
	hash := deployed(t, fake, fullMeta()...)
	b := &fakeBuilder{result: &buildResult{Artifacts: []builtArtifact{{Path: "a.wasm", SHA256: hash}}}}

	out, err := runVerify(t, fake, b, stellartest.UndeployedContract,
		"--source", "https://example.com/fork", "--rev", "v2", "--image", "example/build:1.0")
	require.NoError(t, err)

	assert.Equal(t, buildRequest{Source: "https://example.com/fork", Rev: "v2", Image: "example/build:1.0"}, b.got[0])
	assert.Contains(t, out, "not pinned by digest")
}

func TestVerifyPrefersSourceRevOverCommitHash(t *testing.T) {
	fake := stellartest.NewFake(t)
	hash := deployed(t, fake,
		abitest.Meta{Key: "source_repo", Val: repoURL},
		abitest.Meta{Key: "commit_hash", Val: "from-commit"},
		abitest.Meta{Key: "source_rev", Val: "from-rev"},
		abitest.Meta{Key: "bldimg", Val: pinnedImage})
	b := &fakeBuilder{result: &buildResult{Artifacts: []builtArtifact{{Path: "a.wasm", SHA256: hash}}}}

	_, err := runVerify(t, fake, b, stellartest.UndeployedContract)
	require.NoError(t, err)
	assert.Equal(t, "from-rev", b.got[0].Rev)
}

func TestResolveBuildEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		meta      map[string]string
		source    string
		rev       string
		image     string
		want      buildRequest
		wantError string
	}{
		{
			name:      "empty metadata",
			meta:      map[string]string{},
			wantError: "no source repository",
		},
		{
			name: "commit hash fallback",
			meta: map[string]string{
				"source_repo": repoURL,
				"commit_hash": "abc123",
				"bldimg":      pinnedImage,
			},
			want: buildRequest{Source: repoURL, Rev: "abc123", Image: pinnedImage},
		},
		{
			name: "option-like metadata",
			meta: map[string]string{
				"source_repo": "--upload-pack=evil",
				"commit_hash": "abc123",
				"bldimg":      pinnedImage,
			},
			wantError: "looks like an option",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBuild(tt.meta, tt.source, tt.rev, tt.image)
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVerifyJSON(t *testing.T) {
	fake := stellartest.NewFake(t)
	hash := deployed(t, fake, fullMeta()...)
	b := &fakeBuilder{result: &buildResult{
		Toolchain: toolchain{Rustc: "rustc 1.91.0"},
		Artifacts: []builtArtifact{{Path: "a.wasm", SHA256: hash}},
	}}

	out, err := runVerify(t, fake, b, "--json", stellartest.UndeployedContract)
	require.NoError(t, err)

	var report verifyReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.True(t, report.Verified)
	assert.True(t, report.ImagePinned)
	assert.Equal(t, hash, report.WasmHash)
	require.Len(t, report.Artifacts, 1)
	assert.True(t, report.Artifacts[0].Match)
}

func TestVerifyNeedsAnImageAndNeverGuessesOne(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake,
		abitest.Meta{Key: "source_repo", Val: repoURL},
		abitest.Meta{Key: "commit_hash", Val: "abc123"})
	b := &fakeBuilder{}

	_, err := runVerify(t, fake, b, stellartest.UndeployedContract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no build image")
	assert.Contains(t, err.Error(), "--image")
	assert.Empty(t, b.got, "nothing is built without an image")
}

func TestVerifyReportsEachMissingInput(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake)

	_, err := runVerify(t, fake, &fakeBuilder{}, stellartest.UndeployedContract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no source repository")

	_, err = runVerify(t, fake, &fakeBuilder{}, stellartest.UndeployedContract, "--source", repoURL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no revision")
}

func TestVerifyRejectsValuesThatLookLikeOptions(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake, fullMeta()...)

	for _, flag := range []string{"--source", "--rev", "--image"} {
		b := &fakeBuilder{}
		_, err := runVerify(t, fake, b, stellartest.UndeployedContract, flag+"=--upload-pack=evil")
		require.Error(t, err, flag)
		assert.Contains(t, err.Error(), flag)
		assert.Empty(t, b.got, flag)
	}
}

func TestVerifyRejectsOptionLikeMetadata(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake,
		abitest.Meta{Key: "source_repo", Val: "--upload-pack=evil"},
		abitest.Meta{Key: "commit_hash", Val: "abc123"},
		abitest.Meta{Key: "bldimg", Val: pinnedImage})
	b := &fakeBuilder{}

	_, err := runVerify(t, fake, b, stellartest.UndeployedContract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looks like an option")
	assert.Empty(t, b.got)
}

func TestVerifyStellarAssetContractHasNothingToVerify(t *testing.T) {
	_, err := runVerify(t, stellartest.NewFake(t), &fakeBuilder{}, stellartest.SACContract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stellar asset contract")
}

func TestVerifyBuilderFailureIsARuntimeError(t *testing.T) {
	fake := stellartest.NewFake(t)
	deployed(t, fake, fullMeta()...)

	_, err := runVerify(t, fake, &fakeBuilder{err: errors.New("docker is not installed")}, stellartest.UndeployedContract)
	require.Error(t, err)
	var failed *checkFailedError
	assert.False(t, errors.As(err, &failed), "a tool failure is exit 2, not a verification failure")
}

func TestVerifyRejectsABadContractID(t *testing.T) {
	_, err := runVerify(t, stellartest.NewFake(t), &fakeBuilder{}, "nope")
	require.Error(t, err)
}

func TestCollectWasmOnlyTakesReleaseArtifacts(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"target/wasm32v1-none/release/token.wasm":      "token",
		"target/wasm32v1-none/release/deps/token.wasm": "intermediate",
		"target/wasm32v1-none/debug/token.wasm":        "debug",
		"target/wasm32v1-none/release/notes.txt":       "text",
	} {
		writeFile(t, root, path, content)
	}

	artifacts, err := collectWasm(root)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	assert.Equal(t, "target/wasm32v1-none/release/token.wasm", artifacts[0].Path)
	assert.Len(t, artifacts[0].SHA256, 64)

	none, err := collectWasm(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, none)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
