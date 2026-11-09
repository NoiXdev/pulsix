package pb_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// fullJob returns a Job with every field set, including all four maps.
func fullJob() *workerpb.Job {
	return &workerpb.Job{
		Id:          "0198f0a1-7c2d-7000-8000-000000000001",
		Kind:        "run",
		OrgId:       "0198f0a1-7c2d-7000-8000-000000000002",
		ZoneId:      "0198f0a1-7c2d-7000-8000-000000000003",
		TemplateId:  "0198f0a1-7c2d-7000-8000-000000000004",
		Playbook:    "repo:0198f0a1-7c2d-7000-8000-000000000005:site.yml",
		Limit:       "web-1",
		ExtraVars:   map[string]string{"release": "v1.2.3"},
		SshUsername: "deploy",
		InventoryDoc: `{"all":{"hosts":{"web-1":` +
			`{"ansible_host":"10.0.0.1"}}}}`,
		Connections: map[string]string{"web-1": `{"host":"10.0.0.1","port":22}`},
		Credentials: map[string]string{
			"0198f0a1-7c2d-7000-8000-000000000006": `{"username":"deploy","password":"s3cret"}`,
		},
		HostKeys:    map[string]string{"web-1": "SHA256:AbCdEf0123456789"},
		KindPayload: `{"login_user":"setup","login_password":"hunter2"}`,
		WorkerToken: "d0c8a1f2b3c4d5e6f708192a3b4c5d6e",
	}
}

func TestJobRoundTrip(t *testing.T) {
	t.Parallel()
	job := fullJob()
	b, err := proto.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	var got workerpb.Job
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}
	if !proto.Equal(job, &got) {
		t.Errorf("round-tripped job differs:\nwant %+v\ngot  %+v", job, &got)
	}
	// Spot-check every field survives, maps included.
	checks := map[string]bool{
		"id":           got.Id == job.Id,
		"kind":         got.Kind == job.Kind,
		"org_id":       got.OrgId == job.OrgId,
		"zone_id":      got.ZoneId == job.ZoneId,
		"template_id":  got.TemplateId == job.TemplateId,
		"playbook":     got.Playbook == job.Playbook,
		"limit":        got.Limit == job.Limit,
		"ssh_username": got.SshUsername == job.SshUsername,
		"inventory":    got.InventoryDoc == job.InventoryDoc,
		"kind_payload": got.KindPayload == job.KindPayload,
		"worker_token": got.WorkerToken == job.WorkerToken,
		"extra_vars":   got.ExtraVars["release"] == "v1.2.3" && len(got.ExtraVars) == 1,
		"connections":  got.Connections["web-1"] == job.Connections["web-1"] && len(got.Connections) == 1,
		"credentials":  got.Credentials["0198f0a1-7c2d-7000-8000-000000000006"] == job.Credentials["0198f0a1-7c2d-7000-8000-000000000006"] && len(got.Credentials) == 1,
		"host_keys":    got.HostKeys["web-1"] == "SHA256:AbCdEf0123456789" && len(got.HostKeys) == 1,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("field %q did not survive the round trip", name)
		}
	}
}

func TestRunEventRoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 14, 8, 15, 42, 123000000, time.UTC)
	ev := &workerpb.RunEvent{
		JobId:   "0198f0a1-7c2d-7000-8000-000000000001",
		Seq:     7,
		Host:    "web-1",
		Task:    "Install nginx",
		State:   "ok",
		Payload: `{"rc":0}`,
		At:      timestamppb.New(at),
	}
	b, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal run event: %v", err)
	}
	var got workerpb.RunEvent
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal run event: %v", err)
	}
	if !proto.Equal(ev, &got) {
		t.Errorf("round-tripped event differs:\nwant %+v\ngot  %+v", ev, &got)
	}
	if got.At == nil {
		t.Fatal("timestamp lost in round trip")
	}
	if !got.At.AsTime().Equal(at) {
		t.Errorf("timestamp = %v, want %v", got.At.AsTime(), at)
	}
	if got.Seq != 7 {
		t.Errorf("seq = %d, want 7", got.Seq)
	}
}

// minimalServer implements WorkerServiceServer without embedding
// UnimplementedWorkerServiceServer — allowed because the generated code was
// produced with require_unimplemented_servers=false.
type minimalServer struct {
	workerpb.WorkerServiceServer
}

func (minimalServer) ClaimJob(context.Context, *workerpb.ClaimJobRequest) (*workerpb.ClaimJobResponse, error) {
	return &workerpb.ClaimJobResponse{}, nil
}
func (minimalServer) RunEvents(workerpb.WorkerService_RunEventsServer) error {
	return nil
}
func (minimalServer) ReportResult(context.Context, *workerpb.ReportResultRequest) (*workerpb.ReportResultResponse, error) {
	return &workerpb.ReportResultResponse{}, nil
}

func TestRegisterWorkerServiceServer(t *testing.T) {
	t.Parallel()

	// The generated server interface exposes exactly the three job RPCs,
	// Enroll and RenewCertificate (v0.2c) and ListRepositories (7.95).
	if got := reflect.TypeOf((*workerpb.WorkerServiceServer)(nil)).Elem().NumMethod(); got != 6 {
		t.Errorf("WorkerServiceServer has %d methods, want 6", got)
	}
	for _, name := range []string{"ClaimJob", "RunEvents", "ReportResult", "Enroll", "RenewCertificate", "ListRepositories"} {
		_, ok := reflect.TypeOf((*workerpb.WorkerServiceServer)(nil)).Elem().MethodByName(name)
		if !ok {
			t.Errorf("WorkerServiceServer is missing method %q", name)
		}
	}
	// grpc.NewServer accepts the generated Unimplemented server...
	unimplemented := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(unimplemented, workerpb.UnimplementedWorkerServiceServer{})
	unimplemented.Stop()
	// ...and a minimal implementation without the embedded type (the
	// require_unimplemented_servers=false behaviour).
	minimal := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(minimal, minimalServer{})
	minimal.Stop()
}

func TestEnrollMessagesRoundTrip(t *testing.T) {
	t.Parallel()

	req := &workerpb.EnrollRequest{TokenId: "0197c0a2-0000-7000-8000-000000000001", Secret: []byte("s3cr3t"), Csr: []byte("csr"), Version: "v0.2c"}
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var gotReq workerpb.EnrollRequest
	if err := proto.Unmarshal(raw, &gotReq); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(req, &gotReq) {
		t.Errorf("round-tripped enroll request differs:\nwant %+v\ngot  %+v", req, &gotReq)
	}

	resp := &workerpb.RenewCertificateResponse{Certificate: []byte("cert")}
	raw, err = proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var gotResp workerpb.RenewCertificateResponse
	if err := proto.Unmarshal(raw, &gotResp); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(resp, &gotResp) {
		t.Errorf("round-tripped renew response differs:\nwant %+v\ngot  %+v", resp, &gotResp)
	}
}
