// Package gitbundle migrates the CCR seed-bundle creation and upload helpers
// from utils/teleport/gitBundle.ts. Git command execution and Files API upload
// are injected so the fallback/orchestration logic can be unit-tested without a
// real repository or network.
package gitbundle

import (
	"context"
	"fmt"
	"strings"

	"code-cli/internal/filesapi"
)

const (
	// DefaultBundleMaxBytes is the default size cap for seed bundles (100 MiB).
	// Tunable in TypeScript via tengu_ccr_bundle_max_bytes.
	DefaultBundleMaxBytes int64 = 100 * 1024 * 1024

	// SeedStashRef holds the dangling WIP stash so --all/HEAD bundles include it.
	SeedStashRef = "refs/seed/stash"
	// SeedRootRef holds the parentless squashed-root commit for the last fallback.
	SeedRootRef = "refs/seed/root"
	// SeedRelativePath is the fixed Files API relative path CCR looks for.
	SeedRelativePath = "_source_seed.bundle"
)

// BundleScope identifies which fallback tier produced the uploaded seed bundle.
type BundleScope string

const (
	// ScopeAll is a full --all bundle (all refs, including seed stash when present).
	ScopeAll BundleScope = "all"
	// ScopeHead is a HEAD-only bundle (plus seed stash when present).
	ScopeHead BundleScope = "head"
	// ScopeSquashed is a single parentless commit of HEAD/stash tree.
	ScopeSquashed BundleScope = "squashed"
)

// FailReason classifies deterministic failure modes from gitBundle.ts.
type FailReason string

const (
	// FailGitError means a git command failed before a usable bundle existed.
	FailGitError FailReason = "git_error"
	// FailTooLarge means every fallback tier exceeded the size budget.
	FailTooLarge FailReason = "too_large"
	// FailEmptyRepo means the repository has no refs/commits to bundle.
	FailEmptyRepo FailReason = "empty_repo"
)

// BundleUploadResult mirrors BundleUploadResult from gitBundle.ts.
type BundleUploadResult struct {
	Success         bool
	FileID          string
	BundleSizeBytes int64
	Scope           BundleScope
	HasWIP          bool
	Error           string
	FailReason      FailReason
}

// CommandResult is one injected git command outcome.
type CommandResult struct {
	Stdout string
	Stderr string
	Code   int
}

// CommandRunner runs git-compatible commands. Args exclude the git executable.
type CommandRunner interface {
	Run(ctx context.Context, cwd string, args ...string) CommandResult
}

// Uploader uploads a local seed bundle through the Files API.
type Uploader interface {
	UploadFile(ctx context.Context, filePath string, relativePath string) filesapi.UploadResult
}

// FileSystem is the local filesystem surface used by seed-bundle orchestration.
type FileSystem interface {
	StatSize(path string) (int64, error)
	Remove(path string) error
	TempFilePath(prefix, suffix string) string
}

// GitRootFinder locates the repository root for a working directory.
type GitRootFinder interface {
	FindGitRoot(workdir string) (string, bool)
}

// Options configures one create-and-upload call.
type Options struct {
	// CWD is the working directory used to locate the git root. Empty uses ".".
	CWD string
	// MaxBytes overrides DefaultBundleMaxBytes when positive.
	MaxBytes int64
}

// Config wires injectable dependencies for CreateAndUploadGitBundle.
type Config struct {
	Runner    CommandRunner
	Uploader  Uploader
	FS        FileSystem
	GitRoot   GitRootFinder
	GitBinary string
}

// BundleMaxBytes returns the effective size budget, matching the TypeScript
// feature-flag fallback to DEFAULT_BUNDLE_MAX_BYTES.
func BundleMaxBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return DefaultBundleMaxBytes
	}
	return maxBytes
}

// BundleCreateArgs returns the git args for `git bundle create`.
// When hasStash is true and base is not the squashed root, refs/seed/stash is
// appended so WIP remains reachable in --all and HEAD bundles.
func BundleCreateArgs(bundlePath, base string, hasStash bool) []string {
	args := []string{"bundle", "create", bundlePath, base}
	if hasStash && base != SeedRootRef {
		args = append(args, SeedStashRef)
	}
	return args
}

// TreeRefForSquash returns the tree-ish used by commit-tree for the squashed root.
func TreeRefForSquash(hasStash bool) string {
	if hasStash {
		return SeedStashRef + "^{tree}"
	}
	return "HEAD^{tree}"
}

// SeedRefs lists the temporary seed refs that must be cleaned before and after
// a bundle attempt so a crashed prior run cannot pollute --all bundles.
func SeedRefs() []string {
	return []string{SeedStashRef, SeedRootRef}
}

// FormatGitError formats a git failure using the TypeScript message shape.
func FormatGitError(operation string, code int, stderr string) string {
	return fmt.Sprintf("%s failed (%d): %s", operation, code, truncate(stderr, 200))
}

// TooLargeError is the TypeScript too-large message shown to users.
func TooLargeError() string {
	return "Repo is too large to bundle. Please setup GitHub on https://claude.ai/code"
}

// CreateAndUploadGitBundle creates a seed bundle with the --all → HEAD →
// squashed-root fallback chain and uploads it through the Files API.
func CreateAndUploadGitBundle(ctx context.Context, config Config, opts Options) BundleUploadResult {
	if config.Runner == nil {
		return fail("git command runner is required", "")
	}
	if config.Uploader == nil {
		return fail("file uploader is required", "")
	}
	if config.FS == nil {
		return fail("filesystem is required", "")
	}
	if config.GitRoot == nil {
		return fail("git root finder is required", "")
	}

	workdir := strings.TrimSpace(opts.CWD)
	if workdir == "" {
		workdir = "."
	}
	gitRoot, ok := config.GitRoot.FindGitRoot(workdir)
	if !ok {
		return fail("Not in a git repository", "")
	}

	// Sweep stale refs from a crashed prior run before --all bundles them.
	deleteSeedRefs(ctx, config.Runner, gitRoot)

	refCheck := config.Runner.Run(ctx, gitRoot, "for-each-ref", "--count=1", "refs/")
	if refCheck.Code == 0 && strings.TrimSpace(refCheck.Stdout) == "" {
		return fail("Repository has no commits yet", FailEmptyRepo)
	}

	stashResult := config.Runner.Run(ctx, gitRoot, "stash", "create")
	wipStashSHA := ""
	if stashResult.Code == 0 {
		wipStashSHA = strings.TrimSpace(stashResult.Stdout)
	}
	hasWIP := wipStashSHA != ""
	if hasWIP {
		_ = config.Runner.Run(ctx, gitRoot, "update-ref", SeedStashRef, wipStashSHA)
	}

	bundlePath := config.FS.TempFilePath("ccr-seed", ".bundle")
	defer func() {
		_ = config.FS.Remove(bundlePath)
		deleteSeedRefs(ctx, config.Runner, gitRoot)
	}()

	maxBytes := BundleMaxBytes(opts.MaxBytes)
	bundle, ok := createBundleWithFallback(ctx, config, gitRoot, bundlePath, maxBytes, hasWIP)
	if !ok {
		return BundleUploadResult{
			Success:    false,
			Error:      bundle.Error,
			FailReason: bundle.FailReason,
			HasWIP:     hasWIP,
		}
	}

	upload := config.Uploader.UploadFile(ctx, bundlePath, SeedRelativePath)
	if !upload.Success {
		return BundleUploadResult{
			Success: false,
			Error:   upload.Error,
			HasWIP:  hasWIP,
		}
	}

	return BundleUploadResult{
		Success:         true,
		FileID:          upload.FileID,
		BundleSizeBytes: upload.Size,
		Scope:           bundle.Scope,
		HasWIP:          hasWIP,
	}
}

type createResult struct {
	Scope      BundleScope
	Size       int64
	Error      string
	FailReason FailReason
}

func createBundleWithFallback(ctx context.Context, config Config, gitRoot, bundlePath string, maxBytes int64, hasStash bool) (createResult, bool) {
	if result, ok := tryBundle(ctx, config, gitRoot, bundlePath, "--all", ScopeAll, maxBytes, hasStash); ok || result.FailReason == FailGitError {
		if ok {
			return result, true
		}
		return result, false
	}

	if result, ok := tryBundle(ctx, config, gitRoot, bundlePath, "HEAD", ScopeHead, maxBytes, hasStash); ok || result.FailReason == FailGitError {
		if ok {
			return result, true
		}
		return result, false
	}

	// Last resort: squash to a single parentless commit.
	treeRef := TreeRefForSquash(hasStash)
	commitTree := config.Runner.Run(ctx, gitRoot, "commit-tree", treeRef, "-m", "seed")
	if commitTree.Code != 0 {
		return createResult{
			Error:      FormatGitError("git commit-tree", commitTree.Code, commitTree.Stderr),
			FailReason: FailGitError,
		}, false
	}
	squashedSHA := strings.TrimSpace(commitTree.Stdout)
	_ = config.Runner.Run(ctx, gitRoot, "update-ref", SeedRootRef, squashedSHA)
	if result, ok := tryBundle(ctx, config, gitRoot, bundlePath, SeedRootRef, ScopeSquashed, maxBytes, false); ok {
		return result, true
	} else if result.FailReason == FailGitError {
		return result, false
	}

	return createResult{
		Error:      TooLargeError(),
		FailReason: FailTooLarge,
	}, false
}

func tryBundle(ctx context.Context, config Config, gitRoot, bundlePath, base string, scope BundleScope, maxBytes int64, hasStash bool) (createResult, bool) {
	args := BundleCreateArgs(bundlePath, base, hasStash)
	result := config.Runner.Run(ctx, gitRoot, args...)
	if result.Code != 0 {
		operation := "git bundle create " + base
		switch base {
		case "--all":
			operation = "git bundle create --all"
		case "HEAD":
			operation = "git bundle create HEAD"
		case SeedRootRef:
			operation = "git bundle create " + SeedRootRef
		}
		return createResult{
			Error:      FormatGitError(operation, result.Code, result.Stderr),
			FailReason: FailGitError,
		}, false
	}
	size, err := config.FS.StatSize(bundlePath)
	if err != nil {
		return createResult{
			Error:      err.Error(),
			FailReason: FailGitError,
		}, false
	}
	if size <= maxBytes {
		return createResult{Scope: scope, Size: size}, true
	}
	return createResult{Size: size}, false
}

func deleteSeedRefs(ctx context.Context, runner CommandRunner, gitRoot string) {
	for _, ref := range SeedRefs() {
		_ = runner.Run(ctx, gitRoot, "update-ref", "-d", ref)
	}
}

func fail(message string, reason FailReason) BundleUploadResult {
	return BundleUploadResult{Success: false, Error: message, FailReason: reason}
}

func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
