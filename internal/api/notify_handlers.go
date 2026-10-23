package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type notifyChannelListOutput struct {
	Body struct {
		Channels      []notifyChannelDTO `json:"channels"`
		MailAvailable bool               `json:"mail_available" doc:"Whether this instance has outgoing mail settings (e-mail channels need them)"`
	}
}

type notifyChannelIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type notifyChannelCreateInput struct {
	Org  string `path:"org"`
	Body NotifyChannelBody
}

type notifyChannelUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The channel's ETag; required"`
	Body    NotifyChannelBody
}

type notifyChannelOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Channel notifyChannelDTO `json:"channel"`
	}
}

type notifyDeliveriesInput struct {
	Org   string `path:"org"`
	ID    string `path:"id"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
}

type notifyDeliveriesOutput struct {
	Body struct {
		Deliveries []notifyDeliveryDTO `json:"deliveries"`
	}
}

type notifyDeliveryInput struct {
	Org      string `path:"org"`
	ID       string `path:"id"`
	Delivery string `path:"delivery"`
}

type notifyDeliveryOutput struct {
	Body struct {
		Delivery notifyDeliveryDTO `json:"delivery"`
		// Payload is the stored event exactly as it was sent, not a
		// rendering of it: a custom body template is applied by the
		// sender, so this is the input every channel's message was
		// built from.
		Payload json.RawMessage `json:"payload"`
	}
}

type notifyTestOutput struct {
	Body struct {
		Delivery notifyDeliveryDTO `json:"delivery"`
	}
}

// registerNotifyChannelRoutes wires spec §5's channels: channels.read
// (operator) to see them and their delivery log, channels.write (admin)
// to manage and test them.
func registerNotifyChannelRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/notification-channels", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*notifyChannelListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Notify.ListChannels(ctx, scope.OrgID)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		mailOK, err := deps.Notify.MailAvailable(ctx)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		out := &notifyChannelListOutput{}
		out.Body.MailAvailable = mailOK
		out.Body.Channels = make([]notifyChannelDTO, 0, len(list))
		for _, c := range list {
			dto, err := toNotifyChannelDTO(c)
			if err != nil {
				return nil, mapNotifyErr(ctx, err)
			}
			out.Body.Channels = append(out.Body.Channels, dto)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/notification-channels", func(ctx context.Context, in *notifyChannelCreateInput) (*notifyChannelOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		c, err := deps.Notify.CreateChannel(ctx, scope.OrgID, scope.UserID, in.Body.input())
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, c.ID.String(), "created notification channel "+c.Name)
		return notifyChannelOut(ctx, c)
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createNotificationChannel", "channel"))

	huma.Get(hapi, "/api/v1/orgs/{org}/notification-channels/{id}", func(ctx context.Context, in *notifyChannelIDInput) (*notifyChannelOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		c, err := deps.Notify.GetChannel(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		return notifyChannelOut(ctx, c)
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/notification-channels/{id}", func(ctx context.Context, in *notifyChannelUpdateInput) (*notifyChannelOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		c, err := deps.Notify.UpdateChannel(ctx, scope.OrgID, id, rev, in.Body.input())
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, c.ID.String(), "updated notification channel "+c.Name)
		return notifyChannelOut(ctx, c)
	}, withAudit("updateNotificationChannel", "channel"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/notification-channels/{id}", func(ctx context.Context, in *notifyChannelIDInput) (*okOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		// Fetched before the delete so the audit summary can still name the
		// channel, matching create/update/test below (whose summaries all
		// name it); once DeleteChannel succeeds there is nothing left to
		// look the name up from.
		c, err := deps.Notify.GetChannel(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		if err := deps.Notify.DeleteChannel(ctx, scope.OrgID, id); err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted notification channel "+c.Name)
		return &okOutput{}, nil
	}, withAudit("deleteNotificationChannel", "channel"))

	huma.Post(hapi, "/api/v1/orgs/{org}/notification-channels/{id}/test", func(ctx context.Context, in *notifyChannelIDInput) (*notifyTestOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		d, err := deps.Notify.SendTest(ctx, notify.OrgRef{ID: scope.OrgID, Slug: scope.Org.Slug, Name: scope.Org.Name}, id)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "sent a test notification ("+string(d.Status)+")")
		out := &notifyTestOutput{}
		out.Body.Delivery = toNotifyDeliveryDTO(d)
		return out, nil
	}, withAudit("testNotificationChannel", "channel"))

	huma.Get(hapi, "/api/v1/orgs/{org}/notification-channels/{id}/deliveries", func(ctx context.Context, in *notifyDeliveriesInput) (*notifyDeliveriesOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		// clampListLimit is the shared 1-100 limit clamp
		// (schedule_handlers.go): the query tag above already rejects an
		// out-of-range value, but Service.Deliveries passes limit straight
		// to Postgres, so this is the defense a value reaching it by any
		// other path still needs.
		list, err := deps.Notify.Deliveries(ctx, scope.OrgID, id, clampListLimit(in.Limit))
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		out := &notifyDeliveriesOutput{}
		out.Body.Deliveries = make([]notifyDeliveryDTO, len(list))
		for i, d := range list {
			out.Body.Deliveries[i] = toNotifyDeliveryDTO(d)
		}
		return out, nil
	})

	// One delivery, payload included (backlog 2.75). Separate from the
	// list because the list returns up to a hundred rows and a digest's
	// event is not small; the payload is what was SENT, so an operator
	// looking at a failure can see it rather than guess from the kind.
	// channels.read, like the log it belongs to.
	huma.Get(hapi, "/api/v1/orgs/{org}/notification-channels/{id}/deliveries/{delivery}", func(ctx context.Context, in *notifyDeliveryInput) (*notifyDeliveryOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		deliveryID, err := parseUUID(in.Delivery, "delivery id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Notify.Delivery(ctx, scope.OrgID, id, deliveryID)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		out := &notifyDeliveryOutput{}
		out.Body.Delivery = toNotifyDeliveryDTO(d)
		out.Body.Payload = d.Payload
		return out, nil
	})

	// Resend (backlog 2.75): channels.write and audited, because it
	// puts organization data in front of an outside receiver — the same
	// reason a test send is an admin action.
	huma.Post(hapi, "/api/v1/orgs/{org}/notification-channels/{id}/deliveries/{delivery}/resend", func(ctx context.Context, in *notifyDeliveryInput) (*notifyTestOutput, error) {
		scope, id, err := notifyChannelScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		deliveryID, err := parseUUID(in.Delivery, "delivery id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Notify.ResendDelivery(ctx, scope.OrgID, id, deliveryID)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "queued delivery "+deliveryID.String()+" ("+string(d.EventKind)+") to be sent again")
		out := &notifyTestOutput{}
		out.Body.Delivery = toNotifyDeliveryDTO(d)
		return out, nil
	}, withAudit("resendNotificationDelivery", "channel"))
}

func notifyChannelScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "channel id")
	return scope, id, err
}

func notifyChannelOut(ctx context.Context, c notify.Channel) (*notifyChannelOutput, error) {
	dto, err := toNotifyChannelDTO(c)
	if err != nil {
		return nil, mapNotifyErr(ctx, err)
	}
	out := &notifyChannelOutput{ETag: revisionETag(c.Revision)}
	out.Body.Channel = dto
	return out, nil
}

func mapNotifyErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, notify.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, notify.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, notify.ErrNameTaken):
		return huma.Error409Conflict("a channel with this name already exists")
	case errors.Is(err, notify.ErrMailUnavailable):
		return huma.Error409Conflict(notify.ErrMailUnavailable.Error())
	case errors.Is(err, notify.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	default:
		slog.ErrorContext(ctx, "unmapped notify error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
