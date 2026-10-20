package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type commandResult struct {
	stdout    string
	stderr    string
	code      int
	truncated bool
}

type captureBuffer struct {
	data      bytes.Buffer
	limit     int
	truncated bool
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.data.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.data.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (s *Store) baseEnv() []string {
	values := make(map[string]string)
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "SYSTEMROOT", "SystemRoot"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			values[key] = value
		}
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if value, ok := os.LookupEnv(key); ok && safeProxyValue(value) {
			values[key] = value
		}
	}
	if _, ok := values["PATH"]; !ok {
		values["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	values["HOME"] = s.homeDir
	values["XDG_CONFIG_HOME"] = filepath.Join(s.homeDir, ".config")
	values["GH_CONFIG_DIR"] = s.ghConfigDir
	values["CODEX_HOME"] = s.codexDir
	values["GIT_CONFIG_NOSYSTEM"] = "1"
	values["GIT_CONFIG_GLOBAL"] = os.DevNull
	values["GIT_TERMINAL_PROMPT"] = "0"
	values["GIT_ALLOW_PROTOCOL"] = "https"
	values["GIT_LFS_SKIP_SMUDGE"] = "1"
	values["LC_ALL"] = "C"
	if s.allowFileRemoteForTests {
		values["GIT_ALLOW_PROTOCOL"] = "https:file"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func safeProxyValue(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.User == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func (s *Store) runCommand(command []string, cwd string, input string, timeout time.Duration, check bool, envExtra map[string]string) (commandResult, error) {
	if len(command) == 0 {
		return commandResult{}, errStore("operation could not be started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	env := envMap(s.baseEnv())
	for key, value := range envExtra {
		if key != "GH_TOKEN" && key != "GITHUB_TOKEN" {
			env[key] = value
		}
	}
	cmd.Env = envList(env)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	stdout := &captureBuffer{limit: maxGitOutput}
	stderr := &captureBuffer{limit: maxGitOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return commandResult{}, errStore("operation timed out")
	}
	result := commandResult{
		stdout:    stdout.data.String(),
		stderr:    stderr.data.String(),
		truncated: stdout.truncated || stderr.truncated,
	}
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.code = exitErr.ExitCode()
	} else {
		return commandResult{}, errStore("operation could not be started")
	}
	if check {
		return commandResult{}, errStore("external operation failed")
	}
	return result, nil
}

func envMap(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func envList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+values[key])
	}
	return entries
}

func (s *Store) executable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", errStore("required executable is unavailable")
	}
	return path, nil
}

func (s *Store) gitCommand(args []string, config []string) ([]string, error) {
	git, err := s.executable("git")
	if err != nil {
		return nil, err
	}
	command := []string{
		git,
		"-c", "core.hooksPath=" + s.hooksDir,
		"-c", "core.sshCommand=",
		"-c", "core.fsmonitor=false",
		"-c", "commit.gpgSign=false",
		"-c", "credential.helper=",
		"-c", "credential.helper=!gh auth git-credential",
		"-c", "http.extraHeader=",
		"-c", "diff.external=",
		"-c", "filter.lfs.clean=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.required=false",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
	}
	if s.allowFileRemoteForTests {
		command = append(command, "-c", "protocol.file.allow=always")
	}
	for _, item := range config {
		command = append(command, "-c", item)
	}
	command = append(command, args...)
	return command, nil
}

func (s *Store) runGit(args []string, cwd string, timeout time.Duration, config []string) (commandResult, error) {
	command, err := s.gitCommand(args, config)
	if err != nil {
		return commandResult{}, err
	}
	return s.runCommand(command, cwd, "", timeout, true, nil)
}

func (s *Store) gitOutput(args []string, cwd string, timeout time.Duration) (string, error) {
	result, err := s.runGit(args, cwd, timeout, nil)
	if err != nil || result.truncated {
		return "", errStore("Git output is invalid")
	}
	return result.stdout, nil
}

func (s *Store) gitHead() (string, error) {
	head, err := s.gitOutput([]string{"rev-parse", "HEAD"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return "", errStore("checkout HEAD is invalid")
	}
	head = strings.TrimSpace(head)
	if !shaRE.MatchString(head) {
		return "", errStore("checkout HEAD is invalid")
	}
	return head, nil
}

func (s *Store) branch() (string, error) {
	branch, err := s.gitOutput([]string{"symbolic-ref", "--short", "HEAD"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return "", errStore("checkout must be on a named branch")
	}
	branch = strings.TrimSpace(branch)
	if !branchRE.MatchString(branch) || strings.HasPrefix(branch, "/") || strings.Contains(branch, "..") {
		return "", errStore("checkout must be on a named branch")
	}
	return branch, nil
}

func (s *Store) verifyRemote(expected string) (string, error) {
	remote, err := s.gitOutput([]string{"remote", "get-url", "origin"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return "", errStore("origin must be an HTTPS GitHub URL")
	}
	remote = strings.TrimSpace(remote)
	pushRemote, err := s.gitOutput([]string{"remote", "get-url", "--push", "origin"}, s.repositoryDir, 2*time.Minute)
	if err != nil || strings.TrimSpace(pushRemote) != remote {
		return "", errStore("origin fetch and push URLs must match")
	}
	if s.allowFileRemoteForTests {
		if strings.HasPrefix(remote, "file://") || filepath.IsAbs(remote) {
			if expected != "" && remote != expected {
				return "", errStore("connected repository does not match the requested repository")
			}
			return remote, nil
		}
	}
	if !githubRemoteRE.MatchString(remote) {
		return "", errStore("origin must be an HTTPS GitHub URL")
	}
	if expected != "" && remote != expected {
		return "", errStore("connected repository does not match the requested GitHub repository")
	}
	return remote, nil
}

func (s *Store) remoteHead(branch string) (string, error) {
	if !branchRE.MatchString(branch) {
		return "", errStore("branch name is invalid")
	}
	output, err := s.gitOutput([]string{"ls-remote", "origin", "refs/heads/" + branch}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return "", errStore("remote branch is unavailable")
	}
	for _, line := range strings.Split(output, "\n") {
		pieces := strings.Fields(line)
		if len(pieces) >= 2 && pieces[1] == "refs/heads/"+branch && shaRE.MatchString(pieces[0]) {
			return pieces[0], nil
		}
	}
	return "", errStore("remote branch is unavailable")
}

func (s *Store) statusPaths(unstagedOnly bool) (map[string]struct{}, error) {
	output, err := s.gitOutput([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return nil, errStore("checkout status is unavailable")
	}
	parts := strings.Split(output, "\x00")
	paths := make(map[string]struct{})
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) < 3 || entry[2] != ' ' {
			continue
		}
		status := entry[:2]
		name := entry[3:]
		if name == "" {
			continue
		}
		if !unstagedOnly || status[1] != ' ' {
			paths[name] = struct{}{}
		}
		if (status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C') && i+1 < len(parts) {
			if !unstagedOnly || status[1] != ' ' {
				if parts[i+1] != "" {
					paths[parts[i+1]] = struct{}{}
				}
			}
			i++
		}
	}
	return paths, nil
}

func (s *Store) stagedPaths() (map[string]struct{}, error) {
	output, err := s.gitOutput([]string{"diff", "--cached", "--name-only", "-z"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return nil, errStore("staged status is unavailable")
	}
	return splitNulPaths(output), nil
}

func (s *Store) commitPaths() (map[string]struct{}, error) {
	output, err := s.gitOutput([]string{"show", "--format=", "--name-only", "--no-renames", "HEAD"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return nil, errStore("commit status is unavailable")
	}
	paths := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths[line] = struct{}{}
		}
	}
	return paths, nil
}

func splitNulPaths(output string) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, item := range strings.Split(output, "\x00") {
		if item != "" {
			paths[item] = struct{}{}
		}
	}
	return paths
}

func (s *Store) requireRepository() error {
	if info, err := os.Lstat(s.repositoryDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errStore("connect a repository first")
	}
	gitInfo, err := os.Lstat(filepath.Join(s.repositoryDir, ".git"))
	if err != nil || gitInfo.Mode()&os.ModeSymlink != 0 || (!gitInfo.IsDir() && !gitInfo.Mode().IsRegular()) {
		return errStore("connect a repository first")
	}
	return nil
}

func (s *Store) requireCleanCheckout() error {
	paths, err := s.statusPaths(false)
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return errStore("checkout must be clean")
	}
	return nil
}

func (s *Store) indexDigest() (string, error) {
	output, err := s.gitOutput([]string{"ls-files", "--stage", "-z"}, s.repositoryDir, 2*time.Minute)
	if err != nil {
		return "", errStore("index status is unavailable")
	}
	digest := sha256.Sum256([]byte(output))
	return hex.EncodeToString(digest[:]), nil
}

func (s *Store) validateFullName(fullName string) (string, string, error) {
	if strings.Count(fullName, "/") != 1 {
		return "", "", errStore("repository must use owner/name")
	}
	owner, name, _ := strings.Cut(fullName, "/")
	if !repositoryNameRE.MatchString(owner) || !repositoryNameRE.MatchString(name) {
		return "", "", errStore("repository name is invalid")
	}
	return owner, name, nil
}

func (s *Store) validateTarget(target string, allowMissing bool) (string, error) {
	if target == "" || strings.IndexByte(target, 0) >= 0 || strings.Contains(target, "\\") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") || strings.Contains(target, "//") {
		return "", errStore("target path is invalid")
	}
	for _, r := range target {
		if r < 32 || r == 127 {
			return "", errStore("target path is invalid")
		}
	}
	parts := strings.Split(target, "/")
	if len(parts) == 0 {
		return "", errStore("target path is invalid")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errStore("target path is invalid")
		}
	}
	if _, ok := canonicalTopLevel[parts[0]]; !ok {
		return "", errStore("target must be a canonical or inbox path")
	}
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == "tools" || lower == "config" || strings.HasPrefix(lower, "config.") {
			return "", errStore("target path is reserved")
		}
	}
	relative := strings.Join(parts, "/")
	candidate := filepath.Join(append([]string{s.repositoryDir}, parts...)...)
	parent := filepath.Dir(candidate)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", errStore("target parent directory is unavailable")
	}
	current := s.repositoryDir
	for _, part := range parts {
		current = filepath.Join(current, part)
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errStore("target path must not contain symlinks")
		}
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !isWithin(resolvedParent, s.repositoryDir) && filepath.Clean(resolvedParent) != filepath.Clean(s.repositoryDir) {
		return "", errStore("target path escapes the checkout")
	}
	if info, statErr := os.Lstat(candidate); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errStore("target must be a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return "", errStore("target path is unavailable")
	} else if !allowMissing {
		return "", errStore("target does not exist")
	}
	return relative, nil
}

func (s *Store) isSensitivePath(relative string) bool {
	normalized, err := s.validateTarget(relative, true)
	if err != nil {
		return true
	}
	file := filepath.Join(s.repositoryDir, filepath.FromSlash(normalized))
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return true
	}
	if len(data) > 16*1024 {
		data = data[:16*1024]
	}
	if !utf8Valid(data) {
		return true
	}
	return contentDeclaresSensitive(string(data))
}

func contentDeclaresSensitive(content string) bool {
	if !strings.HasPrefix(content, "---\n") {
		return false
	}
	end := strings.Index(content[4:], "\n---")
	if end >= 0 {
		end += 4
	} else {
		end = len(content)
	}
	for _, line := range strings.Split(content[4:end], "\n") {
		if strings.HasPrefix(line, "sensitivity:") {
			value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "sensitivity:")), "\"'")
			return strings.EqualFold(value, "sensitive")
		}
	}
	return false
}

func utf8Valid(data []byte) bool {
	return utf8.Valid(data)
}
