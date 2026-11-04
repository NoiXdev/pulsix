package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// writeGateRefusal answers a refusal of a require* gate on a route outside
// huma (the backup download, the API explorer and its spec) in the problem
// shape every huma route answers with. The detail is part of the contract
// — the SPA matches on password_change_required and step_up_required — so
// it must reach the client here too, not only the status. An error that is
// not a huma problem answers 401.
func writeGateRefusal(w http.ResponseWriter, err error) {
	var problem *huma.ErrorModel
	if !errors.As(err, &problem) {
		problem = &huma.ErrorModel{Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: "login required"}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
