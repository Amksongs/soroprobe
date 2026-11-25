package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// builder rebuilds a contract from source inside a container image. It is an
// interface so tests can stand in for git and docker.
type builder interface {
	Build(ctx context.Context, req buildRequest) (*buildResult, error)
}

type buildRequest struct {
	Source string
	Rev    string
	Image  string
}

type buildResult struct {
	Toolchain toolchain
	// Artifacts are the .wasm files the build produced.
	Artifacts []builtArtifact
}

type toolchain struct {
	Rustc   string `json:"rustc,omitempty"`
	Stellar string `json:"stellar,omitempty"`
}

type builtArtifact struct {
	// Path is relative to the repository root.
	Path   string
	SHA256 string
}

// verifyReport is the result of a verify run, and the shape of --json.
type verifyReport struct {
	ContractID string `json:"contract_id"`
	// WasmHash is the sha256 of the Wasm the contract is running.
	WasmHash string `json:"wasm_hash"`
	Source   string `json:"source"`
	Rev      string `json:"rev"`
	Image    string `json:"image"`
	// ImagePinned is true when the image is named by digest (@sha256:...),
	// so rebuilding it later gets the same toolchain. A tag can move.
	ImagePinned bool            `json:"image_pinned"`
	Toolchain   toolchain       `json:"toolchain"`
	Artifacts   []artifactMatch `json:"artifacts"`
	// Verified is true when at least one artifact matches the live Wasm.
	Verified bool `json:"verified"`
}

type artifactMatch struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Match  bool   `json:"match"`
}

func newVerifyCmd(opts *options) *cobra.Command {
	var source, rev, image string

	cmd := &cobra.Command{
		Use:   "verify <contract-id> [--source <repo> --rev <rev> --image <image>]",
		Short: "Rebuild a contract from source and compare it with what is deployed",
		Long: `Rebuild a contract from its source repository inside a container image and
compare the Wasm it produces with the Wasm the contract is running.

--source, --rev and --image default to the build metadata stored in the
contract's Wasm: source_repo, source_rev (or commit_hash) and bldimg. There is
no default image: a build is only as trustworthy as its toolchain, so the image
must come from the contract's metadata or from you. Prefer an image pinned by
digest (name@sha256:...); a tag can move, and the report says which you used.

The repository is cloned and checked out at --rev, then the image runs
"stellar contract build" over it with docker. Every .wasm the build produces
is hashed and compared with the live Wasm. Needs git and docker on the PATH.
This is the one command that runs other programs: it executes the build image
you name, so only verify images you trust.

Exits 0 when at least one built artifact matches, 1 when none does, and 2 when
SoroProbe itself could not run. --json prints the report as JSON.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked first, so a value that would be read as an option by
			// git or docker is refused before anything is run.
			for _, f := range []struct{ flag, value string }{{"--source", source}, {"--rev", rev}, {"--image", image}} {
				if strings.HasPrefix(f.value, "-") {
					return fmt.Errorf("%s %q looks like an option, not a value", f.flag, f.value)
				}
			}
			if _, err := stellar.ParseContractID(args[0]); err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			wasm, hash, err := p.Code(cmd.Context(), args[0])
			if errors.Is(err, abi.ErrNoWasm) {
				return fmt.Errorf("%s is a stellar asset contract: it is built into the host and has no Wasm to verify", args[0])
			}
			if err != nil {
				return err
			}

			meta, err := abi.ParseMeta(wasm)
			if err != nil {
				return err
			}
			req, err := resolveBuild(meta, source, rev, image)
			if err != nil {
				return err
			}

			var b builder = gitDockerBuilder{}
			if opts.builder != nil {
				b = opts.builder
			}
			built, err := b.Build(cmd.Context(), req)
			if err != nil {
				return err
			}

			report := verifyReport{
				ContractID:  args[0],
				WasmHash:    hash,
				Source:      req.Source,
				Rev:         req.Rev,
				Image:       req.Image,
				ImagePinned: strings.Contains(req.Image, "@sha256:"),
				Toolchain:   built.Toolchain,
				Artifacts:   []artifactMatch{},
			}
			for _, a := range built.Artifacts {
				match := strings.EqualFold(a.SHA256, hash)
				report.Verified = report.Verified || match
				report.Artifacts = append(report.Artifacts, artifactMatch{Path: a.Path, SHA256: a.SHA256, Match: match})
			}

			if opts.jsonOut {
				err = writeJSON(cmd.OutOrStdout(), report)
			} else {
				err = renderVerify(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return err
			}
			if !report.Verified {
				return &checkFailedError{msg: fmt.Sprintf("no built artifact matches the Wasm deployed at %s", args[0])}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "git repository to build (default: the contract's source_repo metadata)")
	cmd.Flags().StringVar(&rev, "rev", "", "commit, tag or branch to build (default: the contract's source_rev or commit_hash metadata)")
	cmd.Flags().StringVar(&image, "image", "", "container image to build in (default: the contract's bldimg metadata; there is no built-in default)")
	return cmd
}

// resolveBuild fills what the flags leave empty from the contract's metadata.
// Flags always win. The same option check applies to metadata, because it is
// read from a contract that anyone could have deployed.
func resolveBuild(meta map[string]string, source, rev, image string) (buildRequest, error) {
	req := buildRequest{Source: source, Rev: rev, Image: image}
	if req.Source == "" {
		req.Source = meta["source_repo"]
	}
	if req.Rev == "" {
		req.Rev = meta["source_rev"]
		if req.Rev == "" {
			req.Rev = meta["commit_hash"]
		}
	}
	if req.Image == "" {
		req.Image = meta["bldimg"]
	}

	for _, f := range []struct{ name, flag, key, value string }{
		{"source repository", "--source", "source_repo", req.Source},
		{"revision", "--rev", "source_rev or commit_hash", req.Rev},
		{"build image", "--image", "bldimg", req.Image},
	} {
		if f.value == "" {
			return buildRequest{}, fmt.Errorf("no %s: pass %s (the contract's metadata has no %s)", f.name, f.flag, f.key)
		}
		if strings.HasPrefix(f.value, "-") {
			return buildRequest{}, fmt.Errorf("the contract's %s metadata %q looks like an option, not a value", f.key, f.value)
		}
	}
	return req, nil
}

func renderVerify(w io.Writer, r verifyReport) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n", s.bold("contract"), r.ContractID)
	bw.printf("%s %s\n\n", s.bold("deployed"), r.WasmHash)
	bw.printf("  %-10s %s\n", "source", r.Source)
	bw.printf("  %-10s %s\n", "rev", r.Rev)
	pinned := s.green("pinned by digest")
	if !r.ImagePinned {
		pinned = s.red("not pinned by digest") + s.dim(" (a tag can move; the toolchain may differ on a rebuild)")
	}
	bw.printf("  %-10s %s  %s\n", "image", r.Image, pinned)
	if r.Toolchain.Rustc != "" {
		bw.printf("  %-10s %s\n", "rustc", r.Toolchain.Rustc)
	}
	if r.Toolchain.Stellar != "" {
		bw.printf("  %-10s %s\n", "stellar", r.Toolchain.Stellar)
	}

	bw.printf("\n%s\n", s.bold("built artifacts"))
	if len(r.Artifacts) == 0 {
		bw.printf("  %s\n", s.dim("the build produced no .wasm files"))
	}
	for _, a := range r.Artifacts {
		verdict := s.red("MISMATCH")
		if a.Match {
			verdict = s.green("MATCH   ")
		}
		bw.printf("  %s  %s  %s\n", verdict, a.SHA256, a.Path)
	}

	bw.printf("\n%s ", s.bold("result"))
	if r.Verified {
		bw.printf("%s\n", s.green("VERIFIED  the deployed Wasm was reproduced from source"))
	} else {
		bw.printf("%s\n", s.red("NOT VERIFIED  no built artifact matches the deployed Wasm"))
	}
	return bw.err
}

// gitDockerBuilder is the real builder: git for the source, docker for the
// toolchain.
type gitDockerBuilder struct{}

// Build clones the request's repository at its revision and rebuilds it inside
// the request's container image, returning the toolchain it used and every
// .wasm artifact the build produced.
func (gitDockerBuilder) Build(ctx context.Context, req buildRequest) (*buildResult, error) {
	dir, err := os.MkdirTemp("", "soroprobe-verify-")
	if err != nil {
		return nil, err
	}
	// Best effort: a container running as root may have left files behind.
	defer func() { _ = os.RemoveAll(dir) }()
	src := filepath.Join(dir, "src")

	// "--" ends option parsing, so the repository can never be read as a flag.
	if err := run(ctx, "", "git", "clone", "--", req.Source, src); err != nil {
		return nil, fmt.Errorf("clone %s: %w", req.Source, err)
	}
	if err := run(ctx, src, "git", "checkout", "--detach", req.Rev, "--"); err != nil {
		return nil, fmt.Errorf("check out %s: %w", req.Rev, err)
	}

	var result buildResult
	if result.Toolchain.Rustc, err = output(ctx, "docker", "run", "--rm", req.Image, "rustc", "--version"); err != nil {
		return nil, fmt.Errorf("run %s: %w", req.Image, err)
	}
	if result.Toolchain.Stellar, err = output(ctx, "docker", "run", "--rm", req.Image, "stellar", "--version"); err != nil {
		return nil, fmt.Errorf("run %s: %w", req.Image, err)
	}
	if err := run(ctx, "", "docker", "run", "--rm", "-v", src+":/workspace", "-w", "/workspace",
		req.Image, "stellar", "contract", "build"); err != nil {
		return nil, fmt.Errorf("build in %s: %w", req.Image, err)
	}

	result.Artifacts, err = collectWasm(src)
	return &result, err
}

// run executes a program, sending its output to stderr so that stdout holds
// only the report.
func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the operator names the repository and image; flags are rejected if they start with "-" and "--" ends git option parsing
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file:git:http:https:ssh")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// output runs a program and returns its trimmed standard output.
func output(ctx context.Context, name string, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the operator names the image, and a value starting with "-" is rejected
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// collectWasm hashes every target/**/release/*.wasm under root. The deps
// directory beside it holds intermediate objects, not contract builds, and is
// skipped because its parent is not named release.
func collectWasm(root string) ([]builtArtifact, error) {
	var artifacts []builtArtifact
	err := filepath.WalkDir(filepath.Join(root, "target"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".wasm" || filepath.Base(filepath.Dir(path)) != "release" {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: the path was found by walking the build output
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, builtArtifact{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, err
}
