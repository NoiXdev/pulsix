// The playbook texts a repo sync sent beside its variable sources report
// (backlog 3.127): RecordVarSources stores each reported playbook's text,
// or why the sync sent none, with the path's row, and PlaybookText reads
// one back.
package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/varsource"
)

// PlaybookText is one playbook row's text as the sync that wrote it sent
// it. Content is the text, nil when the sync sent none; LeftOut is then
// why (a varsource.Text* reason), or "" when the sync named no reason
// either (a worker older than the texts).
type PlaybookText struct {
	Content *string
	LeftOut string
}

// StoredPlaybookText is one playbook path's text with the record it
// belongs to. Kept: the record's sync left the path unread (a cap), so
// its row, text included, is from an earlier read than Commit.
type StoredPlaybookText struct {
	Commit   string
	SyncedAt time.Time
	Kept     bool
	PlaybookText
}

// PlaybookText is the stored text of one playbook path of the repository;
// ErrNotFound when no sync recorded the repository or reported the path.
func (s *Service) PlaybookText(ctx context.Context, orgID, repoID uuid.UUID, path string) (StoredPlaybookText, error) {
	return s.store.GetPlaybookText(ctx, orgID, repoID, path)
}

// knownTextReasons are the reasons a sync's left-out text may name.
var knownTextReasons = map[string]bool{
	varsource.TextTooLarge: true, varsource.TextBudget: true,
	varsource.TextNotUTF8: true, varsource.TextUnreadable: true,
}

// reportTexts is the text of each of playbooks, the report's, from raw,
// the texts keys of the sync result (nil: it sent none, every row is
// stored without a text). Paths the report does not name are ignored.
// Nothing a worker sends is trusted: the limits it applies are checked
// again in the report's order — a text over varsource.MaxFileBytes is
// TextTooLarge, one that is not UTF-8 or holds a NUL TextNotUTF8, one past
// the varsource.MaxPlaybookTextsBytes budget TextBudget — and a reason this
// manager does not know is TextUnreadable. A key that does not decode is
// logged and taken as not sent. A path in neither map has no entry.
func (s *Service) reportTexts(ctx context.Context, orgID, repoID uuid.UUID, raw json.RawMessage, playbooks []varsource.Playbook) map[string]PlaybookText {
	if raw == nil {
		return nil
	}
	texts, leftOut, err := decodeTexts(raw)
	if err != nil {
		s.logger().WarnContext(ctx, "playbook texts partly ignored", "org", orgID, "repo", repoID, "err", err)
	}
	out := make(map[string]PlaybookText, len(playbooks))
	budget := varsource.MaxPlaybookTextsBytes
	for _, pb := range playbooks {
		if text, ok := texts[pb.Path]; ok {
			reason := varsource.TextProblem(text)
			if reason == "" {
				if cost := varsource.EncodedTextLen(text); cost <= budget {
					budget -= cost
					out[pb.Path] = PlaybookText{Content: &text}
					continue
				}
				reason = varsource.TextBudget
			}
			out[pb.Path] = PlaybookText{LeftOut: reason}
			continue
		}
		if reason, ok := leftOut[pb.Path]; ok {
			if !knownTextReasons[reason] {
				reason = varsource.TextUnreadable
			}
			out[pb.Path] = PlaybookText{LeftOut: reason}
		}
	}
	return out
}

// decodeTexts is the two texts keys of raw. A key that is absent or does
// not decode is an empty map; err names the keys that did not.
func decodeTexts(raw json.RawMessage) (texts, leftOut map[string]string, err error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, nil, fmt.Errorf("decode the texts: %w", err)
	}
	var errs []error
	decode := func(key string) map[string]string {
		var m map[string]string
		if v, ok := keys[key]; ok {
			if err := json.Unmarshal(v, &m); err != nil {
				errs = append(errs, fmt.Errorf("decode %s: %w", key, err))
				return nil
			}
		}
		return m
	}
	texts = decode(varsource.PlaybookTextsStatsKey)
	leftOut = decode(varsource.PlaybookTextsLeftOutStatsKey)
	return texts, leftOut, errors.Join(errs...)
}

// keptTexts adds to texts the previous text of each of kept, the rows a
// sync keeps from an earlier one.
func keptTexts(texts, prev map[string]PlaybookText, kept []varsource.Playbook) map[string]PlaybookText {
	if len(kept) == 0 {
		return texts
	}
	if texts == nil {
		texts = make(map[string]PlaybookText, len(kept))
	}
	for _, pb := range kept {
		if t, ok := prev[pb.Path]; ok {
			texts[pb.Path] = t
		}
	}
	return texts
}
