// The repo sync's playbook texts (backlog 3.127): when the claim asks for
// them (want_playbook_texts, set by every manager that stores them), the
// executor reads, after the variable sources, each playbook file the report
// names and ships path → text under the result's stats key
// varsource.PlaybookTextsStatsKey, and path → reason for every file it
// does not send under varsource.PlaybookTextsLeftOutStatsKey. An older
// manager never asks, so it never receives either key.
//
// The files are read through the same read root as the sources
// (clonefs.go): a link out of the subpath, or a path into git's own files,
// is unreadable and never read. A text is at most varsource.MaxFileBytes;
// all texts together at most varsource.MaxPlaybookTextsBytes, counted as
// the result carries them (JSON-escaped, varsource.EncodedTextLen). The
// budget is filled in the report's path order, and a later file that still
// fits is kept. A file that is not UTF-8, or holds a NUL byte, is not text
// (varsource.TextProblem).
package worker

import (
	"context"
	"io"
	"io/fs"

	"github.com/noixdev/pulsix/internal/varsource"
)

// readTexts reads the texts of paths, the report's playbook paths, under
// the clone's subpath. A subpath that cannot be opened leaves every path
// unreadable.
func (e *SyncExecutor) readTexts(ctx context.Context, dir, subpath string, paths []string) (texts, leftOut map[string]string) {
	fsys, err := openCloneFS(dir, subpath)
	if err != nil {
		e.logger().WarnContext(ctx, "open the repository subpath for the playbook texts", "err", err)
		leftOut = make(map[string]string, len(paths))
		for _, p := range paths {
			leftOut[p] = varsource.TextUnreadable
		}
		return map[string]string{}, leftOut
	}
	defer func() { _ = fsys.Close() }()
	return readPlaybookTexts(fsys, paths)
}

// readPlaybookTexts reads each of paths from fsys, in order: every path
// ends up either in texts or, with the reason, in leftOut. Neither map is
// nil.
func readPlaybookTexts(fsys fs.FS, paths []string) (texts, leftOut map[string]string) {
	texts, leftOut = map[string]string{}, map[string]string{}
	budget := varsource.MaxPlaybookTextsBytes
	for _, p := range paths {
		if _, done := texts[p]; done {
			continue
		}
		if _, done := leftOut[p]; done {
			continue
		}
		text, reason := readPlaybookText(fsys, p)
		if reason == "" {
			if cost := varsource.EncodedTextLen(text); cost <= budget {
				budget -= cost
				texts[p] = text
				continue
			}
			reason = varsource.TextBudget
		}
		leftOut[p] = reason
	}
	return texts, leftOut
}

// readPlaybookText reads one playbook file: its text, or the reason it has
// none. Only a regular file is opened (a FIFO would block the read), and
// it is read no further than one byte past the per-file limit.
func readPlaybookText(fsys fs.FS, name string) (string, string) {
	if !fs.ValidPath(name) || !varsource.PlaybookPathPattern.MatchString(name) {
		return "", varsource.TextUnreadable
	}
	info, err := fs.Stat(fsys, name)
	if err != nil || !info.Mode().IsRegular() {
		return "", varsource.TextUnreadable
	}
	if info.Size() > varsource.MaxFileBytes {
		return "", varsource.TextTooLarge
	}
	f, err := fsys.Open(name)
	if err != nil {
		return "", varsource.TextUnreadable
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", varsource.TextUnreadable
	}
	data, err := io.ReadAll(io.LimitReader(f, varsource.MaxFileBytes+1))
	if err != nil {
		return "", varsource.TextUnreadable
	}
	text := string(data)
	if reason := varsource.TextProblem(text); reason != "" {
		return "", reason
	}
	return text, ""
}
