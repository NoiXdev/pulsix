package api

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

// parseUUID parses a required id from a path or body, answering 400 with
// the field's name when it is malformed.
func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, huma.Error400BadRequest("invalid " + field)
	}
	return id, nil
}

// parseOptionalUUID parses an optional id; nil or "" means none.
func parseOptionalUUID(s *string, field string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := parseUUID(*s, field)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// optionalUUID parses an optional id carried in a plain request-body string;
// "" means none.
func optionalUUID(s, field string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := parseUUID(s, field)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// optionalString renders an optional id for a response.
func optionalString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}
