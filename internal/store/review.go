package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"folio/internal/storage"
)

// ListProposals returns only validated, non-sensitive review data.
func (s *Store) ListProposals() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		entries, err := os.ReadDir(s.reviewsDir)
		if err != nil {
			return nil, errStore("review data is unavailable")
		}
		type reviewEntry struct {
			name    string
			modTime time.Time
		}
		files := make([]reviewEntry, 0, len(entries))
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, statErr := entry.Info()
			if statErr != nil {
				continue
			}
			files = append(files, reviewEntry{name: entry.Name(), modTime: info.ModTime()})
		}
		sort.Slice(files, func(i, j int) bool {
			if files[i].modTime.Equal(files[j].modTime) {
				return files[i].name > files[j].name
			}
			return files[i].modTime.After(files[j].modTime)
		})
		skipped := 0
		if len(files) > maxReports {
			skipped = len(files) - maxReports
			files = files[:maxReports]
		}
		reports := make([]any, 0, len(files))
		for _, entry := range files {
			reportID := strings.TrimSuffix(entry.name, ".json")
			if !reportIDRE.MatchString(reportID) {
				skipped++
				continue
			}
			value, loadErr := s.loadJSON(filepath.Join(s.reviewsDir, entry.name), maxReportBytes)
			if loadErr != nil {
				skipped++
				continue
			}
			data, proposals, valid := s.validateReport(value, reportID)
			if !valid {
				skipped++
				continue
			}
			report := map[string]any{
				"report_id": reportID,
				"status":    "pending-review",
				"created":   publicText(data["created"], 64),
				"device":    publicText(data["device"], 64),
				"summary":   publicText(data["summary"], maxTextRunes),
				"skipped":   numberValue(data["skipped"]),
				"proposals": proposals,
			}
			reports = append(reports, report)
		}
		return map[string]any{"reports": reports, "count": len(reports), "skipped": skipped}, nil
	})
}

func (s *Store) validateReport(value any, reportID string) (map[string]any, []any, bool) {
	data, ok := value.(map[string]any)
	if !ok || responseString(data, "status") != "pending-review" || strings.EqualFold(strings.TrimSpace(responseString(data, "sensitivity")), "sensitive") {
		return nil, nil, false
	}
	if _, ok := data["summary"].(string); !ok {
		return nil, nil, false
	}
	raw, ok := data["proposals"].([]any)
	if !ok || len(raw) > maxProposals {
		return nil, nil, false
	}
	proposals := make([]any, 0, len(raw))
	for index, candidate := range raw {
		item, ok := candidate.(map[string]any)
		if !ok || len(item) != 5 {
			continue
		}
		keys := []string{"source", "target", "claim_type", "proposal", "reason"}
		valid := true
		values := make(map[string]string, len(keys))
		for _, key := range keys {
			text, textOK := item[key].(string)
			if !textOK || len([]rune(text)) > maxTextRunes {
				valid = false
				break
			}
			values[key] = text
		}
		if !valid {
			continue
		}
		if _, ok := claimTypes[values["claim_type"]]; !ok {
			continue
		}
		source, sourceErr := s.validateTarget(values["source"], true)
		target, targetErr := s.validateTarget(values["target"], true)
		if sourceErr != nil || targetErr != nil || s.isSensitivePath(source) || s.isSensitivePath(target) {
			continue
		}
		proposals = append(proposals, map[string]any{
			"index":      index,
			"source":     source,
			"target":     target,
			"claim_type": values["claim_type"],
			"proposal":   publicText(values["proposal"], maxTextRunes),
			"reason":     publicText(values["reason"], maxTextRunes),
		})
	}
	return data, proposals, true
}

// PreviewChange binds one edited canonical file to the current checkout,
// index, and remote head. It creates a short-lived private review capability.
func (s *Store) PreviewChange(reportID string, proposalIndex int, target, content string) (map[string]any, error) {
	if !reportIDRE.MatchString(reportID) {
		return nil, errStore("review report id is invalid")
	}
	if proposalIndex < 0 || proposalIndex >= maxProposals {
		return nil, errStore("proposal index is invalid")
	}
	if strings.IndexByte(content, 0) >= 0 || len([]byte(content)) > maxContentBytes || !utf8Valid([]byte(content)) {
		return nil, errStore("edited content is invalid")
	}
	return s.withLock(func() (map[string]any, error) {
		if err := s.requireRepository(); err != nil {
			return nil, err
		}
		_, proposals, valid := s.validateReportFile(reportID)
		if !valid {
			return nil, errStore("review report is invalid or unavailable")
		}
		var proposal map[string]any
		for _, value := range proposals {
			item, ok := value.(map[string]any)
			if ok && numberValue(item["index"]) == proposalIndex {
				proposal = item
				break
			}
		}
		if proposal == nil {
			return nil, errStore("proposal index is invalid")
		}
		normalizedTarget, err := s.validateTarget(target, true)
		if err != nil || responseString(proposal, "target") != normalizedTarget {
			return nil, errStore("edited target does not match the reviewed proposal")
		}
		if contentDeclaresSensitive(content) {
			return nil, errStore("sensitive documents cannot be reviewed in Web")
		}
		remoteURL, err := s.verifyRemote(s.configuredRemote())
		if err != nil {
			return nil, err
		}
		targetPath := filepath.Join(s.repositoryDir, filepath.FromSlash(normalizedTarget))
		oldBytes, exists, err := readRegularFile(targetPath, maxContentBytes)
		if err != nil {
			return nil, err
		}
		newBytes := []byte(content)
		if bytesEqual(oldBytes, newBytes) {
			return nil, errStore("edited content does not change the target")
		}
		oldText := string(oldBytes)
		diff := unifiedDiff(normalizedTarget, oldText, content)
		if len([]byte(diff)) > maxDiffBytes {
			return nil, errStore("preview diff is too large")
		}
		baseHead, err := s.gitHead()
		if err != nil {
			return nil, err
		}
		branch, err := s.branch()
		if err != nil {
			return nil, err
		}
		baseRemote, err := s.remoteHead(branch)
		if err != nil {
			return nil, err
		}
		if baseRemote != baseHead {
			return nil, errStore("checkout must match the remote before review")
		}
		indexDigest, err := s.indexDigest()
		if err != nil {
			return nil, err
		}
		reviewID, err := newReviewID()
		if err != nil {
			return nil, errStore("review could not be created")
		}
		expires := time.Now().UTC().Add(reviewTTL)
		review := map[string]any{
			"version":        1,
			"review_id":      reviewID,
			"created_at":     nowString(),
			"expires_at":     expires.Format(time.RFC3339Nano),
			"report_id":      reportID,
			"proposal_index": proposalIndex,
			"claim_type":     responseString(proposal, "claim_type"),
			"target":         normalizedTarget,
			"content":        content,
			"old_sha256":     nullableHash(oldBytes, exists),
			"new_sha256":     sha256Hex(newBytes),
			"index_sha256":   indexDigest,
			"base_head":      baseHead,
			"base_remote":    baseRemote,
			"remote_url":     remoteURL,
			"branch":         branch,
		}
		if err := s.savePrivateJSON(filepath.Join(s.pendingDir, reviewID+".json"), review); err != nil {
			return nil, err
		}
		return map[string]any{
			"status":       "ready",
			"review_id":    reviewID,
			"target":       normalizedTarget,
			"claim_type":   responseString(proposal, "claim_type"),
			"old_sha256":   nullableHash(oldBytes, exists),
			"new_sha256":   sha256Hex(newBytes),
			"index_sha256": indexDigest,
			"base_head":    baseHead,
			"diff":         publicText(diff, maxDiffBytes),
			"expires_at":   expires.Format(time.RFC3339Nano),
		}, nil
	})
}

// ApplyChange consumes one review capability and pushes exactly one reviewed
// file after rechecking all checkout and remote invariants.
func (s *Store) ApplyChange(reviewID, message string) (map[string]any, error) {
	if !reviewIDRE.MatchString(reviewID) {
		return nil, errStore("review id is invalid")
	}
	message = strings.TrimSpace(message)
	if message == "" || len([]byte(message)) > 200 || strings.IndexByte(message, 0) >= 0 {
		return nil, errStore("commit message is invalid")
	}
	for _, r := range message {
		if r < 32 && r != '\n' && r != '\t' {
			return nil, errStore("commit message is invalid")
		}
	}
	return s.withLock(func() (map[string]any, error) {
		reviewPath := filepath.Join(s.pendingDir, reviewID+".json")
		value, err := s.loadJSON(reviewPath, maxContentBytes+64*1024)
		if err != nil {
			return nil, errStore("review id is invalid or expired")
		}
		review, ok := value.(map[string]any)
		if !ok || responseString(review, "review_id") != reviewID {
			return nil, errStore("review id is invalid or expired")
		}
		if expired(responseString(review, "expires_at")) {
			consumeReview(reviewPath)
			return nil, errStore("review has expired")
		}
		if err := s.requireRepository(); err != nil {
			return nil, err
		}
		remoteURL := responseString(review, "remote_url")
		if remoteURL == "" {
			remoteURL = s.configuredRemote()
		}
		verifiedRemote, err := s.verifyRemote(remoteURL)
		if err != nil {
			consumeReview(reviewPath)
			return nil, errStore("origin changed since preview; review again")
		}
		remoteURL = verifiedRemote
		target, err := s.validateTarget(responseString(review, "target"), true)
		if err != nil {
			consumeReview(reviewPath)
			return nil, err
		}
		branch, err := s.branch()
		if err != nil {
			consumeReview(reviewPath)
			return nil, err
		}
		head, err := s.gitHead()
		if err != nil {
			consumeReview(reviewPath)
			return nil, err
		}
		if responseString(review, "branch") != branch || responseString(review, "base_head") != head {
			consumeReview(reviewPath)
			return nil, errStore("checkout changed since preview; review again")
		}
		if err := s.requireCleanCheckout(); err != nil {
			consumeReview(reviewPath)
			return nil, err
		}
		indexDigest, err := s.indexDigest()
		if err != nil || responseString(review, "index_sha256") != indexDigest {
			consumeReview(reviewPath)
			return nil, errStore("index changed since preview; review again")
		}
		targetPath := filepath.Join(s.repositoryDir, filepath.FromSlash(target))
		oldBytes, exists, err := readRegularFile(targetPath, maxContentBytes)
		if err != nil {
			consumeReview(reviewPath)
			return nil, err
		}
		if nullableHash(oldBytes, exists) != nullableString(review["old_sha256"]) {
			consumeReview(reviewPath)
			return nil, errStore("review target changed since preview")
		}
		content, ok := review["content"].(string)
		if !ok || strings.IndexByte(content, 0) >= 0 || !utf8Valid([]byte(content)) {
			consumeReview(reviewPath)
			return nil, errStore("review content is invalid")
		}
		contentBytes := []byte(content)
		if sha256Hex(contentBytes) != responseString(review, "new_sha256") {
			consumeReview(reviewPath)
			return nil, errStore("review content changed")
		}
		remote, err := s.remoteHead(branch)
		if err != nil || !shaRE.MatchString(responseString(review, "base_remote")) || remote != responseString(review, "base_remote") {
			consumeReview(reviewPath)
			return nil, errStore("remote branch changed since preview; review again")
		}
		if remote != head {
			consumeReview(reviewPath)
			return nil, errStore("checkout must match the remote before apply")
		}
		if contentDeclaresSensitive(content) {
			consumeReview(reviewPath)
			return nil, errStore("sensitive documents cannot be reviewed in Web")
		}
		keepWorkingTree := false
		defer func() {
			if keepWorkingTree {
				return
			}
			_, _ = s.runGit([]string{"reset", "--", target}, s.repositoryDir, time.Minute, nil)
			if exists {
				_ = writeAtomic(targetPath, oldBytes, 0o644)
			} else {
				_ = os.Remove(targetPath)
			}
		}()
		if err := writeAtomic(targetPath, contentBytes, 0o644); err != nil {
			return nil, errStore("review target could not be written")
		}
		changed, err := s.statusPaths(false)
		if err != nil || !samePaths(changed, map[string]struct{}{target: {}}) {
			return nil, errStore("checkout contains an unreviewed change")
		}
		if _, err := s.runGit([]string{"add", "--", target}, s.repositoryDir, time.Minute, nil); err != nil {
			return nil, errStore("review could not be staged")
		}
		staged, err := s.stagedPaths()
		if err != nil || !samePaths(staged, map[string]struct{}{target: {}}) {
			return nil, errStore("staged diff contains an unreviewed path")
		}
		unstaged, err := s.statusPaths(true)
		if err != nil || len(unstaged) != 0 {
			return nil, errStore("checkout contains an unreviewed unstaged change")
		}
		verifiedRemote, err = s.verifyRemote(remoteURL)
		if err != nil {
			return nil, errStore("origin changed before commit; no push performed")
		}
		remoteURL = verifiedRemote
		remote, err = s.remoteHead(branch)
		if err != nil || remote != responseString(review, "base_remote") {
			return nil, errStore("remote branch changed before commit; no push performed")
		}
		if _, err := s.runGit([]string{"commit", "--no-verify", "--no-gpg-sign", "-m", message}, s.repositoryDir, 2*time.Minute, []string{
			"user.name=Folio Service",
			"user.email=folio-service@users.noreply.github.com",
		}); err != nil {
			return nil, errStore("reviewed commit failed")
		}
		// A successful commit owns the working tree state. Preserve it even if a
		// later verification step fails, so rollback cannot recreate the old
		// file as an unstaged change on top of the new commit.
		keepWorkingTree = true
		newHead, err := s.gitHead()
		if err != nil {
			return nil, err
		}
		committed, err := s.commitPaths()
		if err != nil || !samePaths(committed, map[string]struct{}{target: {}}) {
			keepWorkingTree = true
			return nil, errStore("commit contains an unreviewed path; no push performed")
		}
		if _, pushErr := s.remote.Publish(context.Background(), executor{s}, storage.Options{
			Dir: s.repositoryDir, Remote: remoteURL, Branch: branch, TestMode: s.allowFileRemoteForTests,
		}); pushErr != nil {
			return nil, errStore("push failed; reviewed local commit retained; no retry performed")
		}
		remote, err = s.remoteHead(branch)
		if err != nil || remote != newHead {
			return nil, errStore("push result could not be verified; inspect the remote")
		}
		consumeReview(reviewPath)
		if err := s.recordRepositoryHead(newHead, branch); err != nil {
			return nil, err
		}
		return map[string]any{"status": "applied", "target": target, "branch": branch, "commit": newHead}, nil
	})
}

func (s *Store) validateReportFile(reportID string) (map[string]any, []any, bool) {
	value, err := s.loadJSON(filepath.Join(s.reviewsDir, reportID+".json"), maxReportBytes)
	if err != nil {
		return nil, nil, false
	}
	data, proposals, valid := s.validateReport(value, reportID)
	return data, proposals, valid
}

func readRegularFile(file string, maxBytes int64) ([]byte, bool, error) {
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return []byte{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, errStore("review target must be a regular file")
	}
	if info.Size() > maxBytes {
		return nil, false, errStore("review target is too large")
	}
	data, err := os.ReadFile(file)
	if err != nil || int64(len(data)) > maxBytes {
		return nil, false, errStore("review target is unavailable")
	}
	if !utf8Valid(data) {
		return nil, false, errStore("review target is not UTF-8 text")
	}
	return data, true, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func nullableHash(data []byte, exists bool) any {
	if !exists {
		return nil
	}
	return sha256Hex(data)
}

func nullableString(value any) any {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func unifiedDiff(target, oldText, newText string) string {
	oldLines := splitLinesKeepEnd(oldText)
	newLines := splitLinesKeepEnd(newText)
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- a/%s\n+++ b/%s\n", target, target)
	// The review path intentionally supports one file and a small bounded edit.
	// A compact whole-file hunk is easier to audit than a dependency-heavy diff.
	fmt.Fprintf(&builder, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, line := range oldLines {
		builder.WriteByte('-')
		builder.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			builder.WriteByte('\n')
		}
	}
	for _, line := range newLines {
		builder.WriteByte('+')
		builder.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func splitLinesKeepEnd(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	return lines
}

func newReviewID() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func expired(value string) bool {
	when, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || !when.After(time.Now().UTC())
}

func consumeReview(file string) { _ = os.Remove(file) }

func samePaths(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

func numberValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	case json.Number:
		var parsed int
		_, _ = fmt.Sscanf(string(number), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}
