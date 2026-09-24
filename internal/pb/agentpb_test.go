package pb_test

import (
	"reflect"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// TestFactsRoundTrip marshals and unmarshals an AgentMessage carrying a
// Facts body with every field set and asserts every field survives.
func TestFactsRoundTrip(t *testing.T) {
	t.Parallel()

	msg := &agentpb.AgentMessage{
		Body: &agentpb.AgentMessage_Facts{
			Facts: &agentpb.Facts{
				Values: map[string]string{
					"os":         "debian-12",
					"kernel":     "6.1.0-18-amd64",
					"agent_ver":  "v0.3a",
					"mem_total":  "16384",
					"python_ver": "3.11.2",
				},
			},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal agent message: %v", err)
	}
	var got agentpb.AgentMessage
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal agent message: %v", err)
	}
	if !proto.Equal(msg, &got) {
		t.Errorf("round-tripped agent message differs:\nwant %+v\ngot  %+v", msg, &got)
	}
	facts := got.GetFacts()
	if facts == nil {
		t.Fatal("facts oneof arm lost in round trip")
	}
	checks := map[string]bool{
		"os":         facts.Values["os"] == "debian-12",
		"kernel":     facts.Values["kernel"] == "6.1.0-18-amd64",
		"agent_ver":  facts.Values["agent_ver"] == "v0.3a",
		"mem_total":  facts.Values["mem_total"] == "16384",
		"python_ver": facts.Values["python_ver"] == "3.11.2",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("facts value %q did not survive the round trip", name)
		}
	}
	if len(facts.Values) != 5 {
		t.Errorf("facts has %d entries, want 5", len(facts.Values))
	}
}

// TestHeartbeatRoundTrip covers the other oneof arm.
func TestHeartbeatRoundTrip(t *testing.T) {
	t.Parallel()

	msg := &agentpb.AgentMessage{
		Body: &agentpb.AgentMessage_Heartbeat{
			Heartbeat: &agentpb.Heartbeat{Version: "v0.3a"},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal agent message: %v", err)
	}
	var got agentpb.AgentMessage
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal agent message: %v", err)
	}
	if !proto.Equal(msg, &got) {
		t.Errorf("round-tripped agent message differs:\nwant %+v\ngot  %+v", msg, &got)
	}
	if hb := got.GetHeartbeat(); hb == nil || hb.Version != "v0.3a" {
		t.Errorf("heartbeat = %+v, want version v0.3a", got.GetHeartbeat())
	}
}

// TestAgentEnrollMessagesRoundTrip covers the enrollment messages.
func TestAgentEnrollMessagesRoundTrip(t *testing.T) {
	t.Parallel()

	req := &agentpb.EnrollRequest{
		Token:   "0197c0a2-0000-7000-8000-000000000001",
		CsrPem:  "-----BEGIN CERTIFICATE REQUEST-----\n...",
		Name:    "edge-1",
		Version: "v0.3a",
	}
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var gotReq agentpb.EnrollRequest
	if err := proto.Unmarshal(raw, &gotReq); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(req, &gotReq) {
		t.Errorf("round-tripped enroll request differs:\nwant %+v\ngot  %+v", req, &gotReq)
	}

	resp := &agentpb.EnrollResponse{
		CertPem: "-----BEGIN CERTIFICATE-----\n...",
		CaPem:   "-----BEGIN CERTIFICATE-----\n...",
		HostId:  "0198f0a1-7c2d-7000-8000-000000000001",
	}
	raw, err = proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var gotResp agentpb.EnrollResponse
	if err := proto.Unmarshal(raw, &gotResp); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(resp, &gotResp) {
		t.Errorf("round-tripped enroll response differs:\nwant %+v\ngot  %+v", resp, &gotResp)
	}

	renew := &agentpb.RenewRequest{CsrPem: "-----BEGIN CERTIFICATE REQUEST-----\n..."}
	raw, err = proto.Marshal(renew)
	if err != nil {
		t.Fatal(err)
	}
	var gotRenew agentpb.RenewRequest
	if err := proto.Unmarshal(raw, &gotRenew); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(renew, &gotRenew) {
		t.Errorf("round-tripped renew request differs:\nwant %+v\ngot  %+v", renew, &gotRenew)
	}

	manager := &agentpb.ManagerMessage{Ack: true}
	raw, err = proto.Marshal(manager)
	if err != nil {
		t.Fatal(err)
	}
	var gotManager agentpb.ManagerMessage
	if err := proto.Unmarshal(raw, &gotManager); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(manager, &gotManager) {
		t.Errorf("round-tripped manager message differs:\nwant %+v\ngot  %+v", manager, &gotManager)
	}
}

func TestRegisterAgentServiceServer(t *testing.T) {
	t.Parallel()

	// The generated server interface exposes exactly the three contract
	// RPCs: Enroll, Renew, AgentStream.
	if got := reflect.TypeOf((*agentpb.AgentServiceServer)(nil)).Elem().NumMethod(); got != 3 {
		t.Errorf("AgentServiceServer has %d methods, want 3", got)
	}
	for _, name := range []string{"Enroll", "Renew", "AgentStream"} {
		_, ok := reflect.TypeOf((*agentpb.AgentServiceServer)(nil)).Elem().MethodByName(name)
		if !ok {
			t.Errorf("AgentServiceServer is missing method %q", name)
		}
	}
	// grpc.NewServer accepts the generated Unimplemented server...
	unimplemented := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(unimplemented, agentpb.UnimplementedAgentServiceServer{})
	unimplemented.Stop()
}
