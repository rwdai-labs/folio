package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"folio/internal/storage"
)

var (
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
)

func validBranch(branch string) bool {
	return branchPattern.MatchString(branch) && !strings.HasPrefix(branch, "/") && !strings.Contains(branch, "..")
}

// Resolve validates owner/name (or a local remote in test mode).
func (p *Plugin) Resolve(input string, testMode bool) (storage.Resolved, error) {
	input = strings.TrimSpace(input)
	if testMode && (strings.HasPrefix(input, "file://") || filepath.IsAbs(input)) {
		return storage.Resolved{Name: filepath.Base(strings.TrimSuffix(input, ".git")), Remote: input}, nil
	}
	if strings.Count(input, "/") != 1 {
		return storage.Resolved{}, errors.New("repository must use owner/name")
	}
	owner, name, _ := strings.Cut(input, "/")
	name = strings.TrimSuffix(name, ".git")
	if !repositoryNameRE.MatchString(owner) || !repositoryNameRE.MatchString(name) {
		return storage.Resolved{}, errors.New("repository name is invalid")
	}
	return storage.Resolved{
		Name:   owner + "/" + name,
		Remote: "https://" + credentialHost + "/" + owner + "/" + name + ".git",
	}, nil
}

// Validate checks that a configured remote belongs to GitHub.
func (p *Plugin) Validate(remote string, testMode bool) error {
	remote = strings.TrimSpace(remote)
	if testMode && (strings.HasPrefix(remote, "file://") || filepath.IsAbs(remote)) {
		return nil
	}
	if !remoteRE.MatchString(remote) {
		return errors.New("origin must be an HTTPS GitHub URL")
	}
	return nil
}

// Sync clones a missing checkout or fast-forwards an existing one.
func (p *Plugin) Sync(ctx context.Context, exec storage.Executor, opts storage.Options) (storage.SyncResult, error) {
	if opts.Dir == "" {
		return storage.SyncResult{}, errors.New("checkout path is invalid")
	}
	credential := p.credentialArgs(exec)
	if _, err := os.Lstat(opts.Dir); os.IsNotExist(err) {
		if opts.Remote == "" {
			return storage.SyncResult{}, errors.New("remote is not configured")
		}
		if err := p.Validate(opts.Remote, opts.TestMode); err != nil {
			return storage.SyncResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(opts.Dir), 0o700); err != nil {
			return storage.SyncResult{}, errors.New("checkout parent directory is unavailable")
		}
		out, runErr := exec.Git(ctx, filepath.Dir(opts.Dir), credential, 10*time.Minute,
			[]string{"clone", "--no-tags", opts.Remote, opts.Dir})
		if runErr != nil || out.Code != 0 {
			return storage.SyncResult{}, errors.New("repository could not be cloned")
		}
		branch, err := p.branch(ctx, exec, opts.Dir, opts.Branch)
		if err != nil {
			return storage.SyncResult{}, err
		}
		head, err := p.head(ctx, exec, opts.Dir)
		if err != nil {
			return storage.SyncResult{}, err
		}
		return storage.SyncResult{Status: "cloned", Branch: branch, Head: head, RemoteHead: head}, nil
	} else if err != nil {
		return storage.SyncResult{}, errors.New("checkout path is unavailable")
	}

	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		out, err := exec.Git(ctx, opts.Dir, nil, 2*time.Minute, []string{"remote", "get-url", "origin"})
		if err != nil || out.Code != 0 {
			return storage.SyncResult{}, errors.New("origin is not configured")
		}
		remote = strings.TrimSpace(out.Stdout)
	}
	if err := p.Validate(remote, opts.TestMode); err != nil {
		return storage.SyncResult{}, err
	}
	branch, err := p.branch(ctx, exec, opts.Dir, opts.Branch)
	if err != nil {
		return storage.SyncResult{}, err
	}
	head, err := p.head(ctx, exec, opts.Dir)
	if err != nil {
		return storage.SyncResult{}, err
	}
	remoteHead, err := p.remoteHead(ctx, exec, remote, branch)
	if err != nil {
		return storage.SyncResult{}, err
	}
	if remoteHead == head {
		return storage.SyncResult{Status: "up_to_date", Branch: branch, Head: head, RemoteHead: remoteHead}, nil
	}
	out, runErr := exec.Git(ctx, opts.Dir, credential, 5*time.Minute,
		[]string{"fetch", "--no-tags", "origin", "refs/heads/" + branch + ":refs/remotes/origin/" + branch})
	if runErr != nil || out.Code != 0 {
		return storage.SyncResult{}, errors.New("repository sync failed")
	}
	out, runErr = exec.Git(ctx, opts.Dir, credential, 2*time.Minute,
		[]string{"merge", "--ff-only", "refs/remotes/origin/" + branch})
	if runErr != nil || out.Code != 0 {
		return storage.SyncResult{}, errors.New("repository sync failed")
	}
	head, err = p.head(ctx, exec, opts.Dir)
	if err != nil {
		return storage.SyncResult{}, err
	}
	return storage.SyncResult{Status: "synced", Branch: branch, Head: head, RemoteHead: remoteHead}, nil
}

// Head reads the remote branch head without touching the checkout.
func (p *Plugin) Head(ctx context.Context, exec storage.Executor, opts storage.Options) (string, error) {
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" && opts.Dir != "" {
		out, err := exec.Git(ctx, opts.Dir, nil, 2*time.Minute, []string{"remote", "get-url", "origin"})
		if err != nil || out.Code != 0 {
			return "", errors.New("origin is not configured")
		}
		remote = strings.TrimSpace(out.Stdout)
	}
	if err := p.Validate(remote, opts.TestMode); err != nil {
		return "", err
	}
	branch, err := p.branch(ctx, exec, opts.Dir, opts.Branch)
	if err != nil {
		return "", err
	}
	return p.remoteHead(ctx, exec, remote, branch)
}

// Publish pushes the committed HEAD and verifies the remote branch.
func (p *Plugin) Publish(ctx context.Context, exec storage.Executor, opts storage.Options) (string, error) {
	if opts.Dir == "" || opts.Remote == "" {
		return "", errors.New("remote is not configured")
	}
	branch, err := p.branch(ctx, exec, opts.Dir, opts.Branch)
	if err != nil {
		return "", err
	}
	head, err := p.head(ctx, exec, opts.Dir)
	if err != nil {
		return "", err
	}
	credential := p.credentialArgs(exec)
	out, runErr := exec.Git(ctx, opts.Dir, credential, 5*time.Minute,
		[]string{"push", "--no-verify", opts.Remote, "HEAD:refs/heads/" + branch})
	if runErr != nil || out.Code != 0 {
		// A failed push may still have landed; verify before reporting failure.
		if remoteHead, verifyErr := p.remoteHead(ctx, exec, opts.Remote, branch); verifyErr != nil || remoteHead != head {
			return "", errors.New("push failed")
		}
	}
	remoteHead, err := p.remoteHead(ctx, exec, opts.Remote, branch)
	if err != nil || remoteHead != head {
		return "", errors.New("push result could not be verified")
	}
	return head, nil
}

func (p *Plugin) branch(ctx context.Context, exec storage.Executor, dir, fallback string) (string, error) {
	if validBranch(fallback) {
		return fallback, nil
	}
	out, err := exec.Git(ctx, dir, nil, 2*time.Minute, []string{"symbolic-ref", "--short", "HEAD"})
	if err != nil || out.Code != 0 {
		return "", errors.New("checkout must be on a named branch")
	}
	branch := strings.TrimSpace(out.Stdout)
	if !validBranch(branch) || strings.HasPrefix(branch, "/") || strings.Contains(branch, "..") {
		return "", errors.New("checkout must be on a named branch")
	}
	return branch, nil
}

func (p *Plugin) head(ctx context.Context, exec storage.Executor, dir string) (string, error) {
	out, err := exec.Git(ctx, dir, nil, 2*time.Minute, []string{"rev-parse", "HEAD"})
	if err != nil || out.Code != 0 {
		return "", errors.New("checkout HEAD is invalid")
	}
	head := strings.TrimSpace(out.Stdout)
	if !shaPattern.MatchString(head) {
		return "", errors.New("checkout HEAD is invalid")
	}
	return head, nil
}

func (p *Plugin) remoteHead(ctx context.Context, exec storage.Executor, remote, branch string) (string, error) {
	if !validBranch(branch) {
		return "", errors.New("branch name is invalid")
	}
	out, err := exec.Git(ctx, p.dataDir, p.credentialArgs(exec), 2*time.Minute,
		[]string{"ls-remote", remote, "refs/heads/" + branch})
	if err != nil || out.Code != 0 {
		return "", errors.New("remote branch is unavailable")
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "refs/heads/"+branch && shaPattern.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
	return "", errors.New("remote branch is unavailable")
}
