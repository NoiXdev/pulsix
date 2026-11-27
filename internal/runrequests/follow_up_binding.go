package runrequests

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// FollowUpBindings is what an approval binds of the follow-up runs it
// starts (backlog 2.143, the owner's decision of 2026-11-27): an entry per
// follow-up of the approved selection, its subtrees included, keyed by
// followUpKey. The claim of a follow-up's job compares the live state with
// its entry (Service.checkFollowUpClaim). Stored with the decision; nil on
// a request approved before it existed, whose follow-ups run unchecked as
// before.
type FollowUpBindings struct {
	Nodes map[string]FollowUpBinding `json:"nodes"`
}

// FollowUpBinding is one follow-up's entry: the fingerprint of its hosts
// and vars as the approval saw them (FollowUpFingerprint) and the hosts
// they were taken from, its parent's bound hosts (the approved run's
// targets for a direct follow-up). Its hosts are its template's targets
// among Within.
type FollowUpBinding struct {
	Fingerprint string      `json:"fingerprint"`
	Within      []uuid.UUID `json:"within"`
}

// FollowUpOrigin is where a follow-up run sits: the run request whose run
// group is the root of its chain, and its own row's chain path and
// template, which key its binding.
type FollowUpOrigin struct {
	RequestID  uuid.UUID
	Path       []uuid.UUID
	TemplateID uuid.UUID
}

// followUpKey keys a follow-up's binding: the templates of its chain path
// (its row's path: the runs before it) and its own, in order. The picture
// walk and the follow-up starter build the same path.
func followUpKey(path []uuid.UUID, templateID uuid.UUID) string {
	parts := make([]string, 0, len(path)+1)
	for _, id := range path {
		parts = append(parts, id.String())
	}
	return strings.Join(append(parts, templateID.String()), "/")
}

// FollowUpFingerprint is the SHA-256 (hex) over the request id and the
// canonical JSON of what n binds: its template, its hosts sorted by id,
// its masked extra vars and its vars digest. Its name, revision and
// follow-ups are not part of it: the claim of the follow-up compares
// exactly its hosts and vars (backlog 2.143).
func FollowUpFingerprint(requestID uuid.UUID, n PictureFollowUp) string {
	targets := slices.Clone(n.Targets)
	slices.SortFunc(targets, func(a, b PictureHost) int { return cmp.Compare(a.ID, b.ID) })
	doc, err := json.Marshal(struct {
		Request    uuid.UUID      `json:"request"`
		TemplateID string         `json:"template_id"`
		Targets    []PictureHost  `json:"targets"`
		ExtraVars  map[string]any `json:"extra_vars"`
		VarsDigest string         `json:"vars_digest"`
	}{requestID, n.TemplateID, targets, n.ExtraVars, n.VarsDigest})
	if err != nil {
		// Only strings, numbers, slices of those and a decoded JSON
		// document; Marshal cannot fail on it.
		panic("follow-up fingerprint: " + err.Error())
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}

// BindFollowUp implements Pictures: the binding of the follow-up
// templateID of request requestID, its hosts taken among within (the
// hosts its parent is bound to), as the picture shows it now.
func (r *Resolver) BindFollowUp(ctx context.Context, orgID, requestID, templateID uuid.UUID, within []uuid.UUID) (PictureFollowUp, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return PictureFollowUp{}, mapNotFound(err)
	}
	return r.bindFollowUp(ctx, orgID, requestID, tmpl, within)
}

// bindFollowUp binds tmpl as a follow-up: its targets under its limit
// that are among within, described like the run's targets and sorted by
// id, its extra vars masked, and the vars digest over those hosts.
func (r *Resolver) bindFollowUp(ctx context.Context, orgID, requestID uuid.UUID, tmpl templates.Template, within []uuid.UUID) (PictureFollowUp, error) {
	scope, err := r.src.Jobs.LimitedRunTargets(ctx, orgID, tmpl.ID)
	if err != nil {
		return PictureFollowUp{}, mapNotFound(err)
	}
	allowed := make(map[uuid.UUID]bool, len(within))
	for _, h := range within {
		allowed[h] = true
	}
	ids := []uuid.UUID{}
	for _, h := range scope {
		if allowed[h] && !slices.Contains(ids, h) {
			ids = append(ids, h)
		}
	}
	// Sorted, so the same hosts read the same way whatever order the scope
	// lists them in (and share one render within a picture, pictureMemo).
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return cmp.Compare(a.String(), b.String()) })
	targets, err := r.describedHosts(ctx, orgID, ids)
	if err != nil {
		return PictureFollowUp{}, err
	}
	slices.SortFunc(targets, func(a, b PictureHost) int { return cmp.Compare(a.ID, b.ID) })
	digest, err := r.varsDigest(ctx, orgID, requestID, tmpl.ID, ids)
	if err != nil {
		return PictureFollowUp{}, err
	}
	n := PictureFollowUp{TemplateID: tmpl.ID.String(), TemplateName: tmpl.Name, Targets: targets, VarsDigest: digest}
	if len(tmpl.ExtraVars) > 0 {
		n.ExtraVars = schema.MaskVars(tmpl.ExtraVars)
	}
	return n, nil
}

// followUpBindings is what an approval stores of the follow-ups it starts
// (started, from p's tree), their subtrees included: each bound node's
// fingerprint and the hosts it was bound within (p's targets for a
// direct follow-up, its parent's bound hosts below).
func followUpBindings(requestID uuid.UUID, p Picture, started []PictureFollowUp) *FollowUpBindings {
	out := &FollowUpBindings{Nodes: map[string]FollowUpBinding{}}
	var walk func(nodes []PictureFollowUp, within []uuid.UUID)
	walk = func(nodes []PictureFollowUp, within []uuid.UUID) {
		for _, n := range nodes {
			id, err := uuid.Parse(n.TemplateID)
			if err != nil || n.Stop != "" || n.VarsDigest == "" {
				// Never started: stopped, or not bound (the setting is off).
				continue
			}
			out.Nodes[followUpKey(n.chainPath, id)] = FollowUpBinding{Fingerprint: FollowUpFingerprint(requestID, n), Within: within}
			walk(n.FollowUps, hostIDsOf(n.Targets))
		}
	}
	walk(started, hostIDsOf(p.Targets))
	return out
}

// hostIDsOf is the ids of hs; the picture writes each from a uuid.UUID,
// so one that does not parse (none) is left out.
func hostIDsOf(hs []PictureHost) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(hs))
	for _, h := range hs {
		if id, err := uuid.Parse(h.ID); err == nil {
			out = append(out, id)
		}
	}
	return out
}
