package services

import (
	"strings"
	"testing"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/delta"
)

// chord service-layer tests keyed to upstream (services/wire.ts,
// state-codec.ts, errors.ts).

func TestServiceControlCalls(t *testing.T) {
	catalogue := DecodeServiceControlCall(CreateServiceCatalogueCall())
	if catalogue == nil || catalogue.Type != "catalogue" {
		t.Fatalf("catalogue = %+v", catalogue)
	}
	subscribe := DecodeServiceControlCall(CreateServiceSubscribeCall("sub-1", "sessions", chord.ServiceModeKeyed))
	if subscribe == nil || subscribe.Type != "subscribe" || subscribe.SubscriptionID != "sub-1" ||
		subscribe.ServiceID != "sessions" || subscribe.Mode != chord.ServiceModeKeyed {
		t.Fatalf("subscribe = %+v", subscribe)
	}
	unsubscribe := DecodeServiceControlCall(CreateServiceUnsubscribeCall("sub-1"))
	if unsubscribe == nil || unsubscribe.Type != "unsubscribe" || unsubscribe.SubscriptionID != "sub-1" {
		t.Fatalf("unsubscribe = %+v", unsubscribe)
	}

	// A non-control call is not a control call.
	if got := DecodeServiceControlCall(chord.ServiceCall{ServiceID: "sessions", Member: "list"}); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
	// A control call with an instance is not a control call.
	call := CreateServiceCatalogueCall()
	call.Instance = &chord.ServiceInstanceAddress{Key: "k", Generation: 1}
	if got := DecodeServiceControlCall(call); got != nil {
		t.Fatalf("instance-bearing control call decoded: %+v", got)
	}
	// A malformed subscribe (bad mode, wrong arity) is rejected.
	bad := chord.ServiceCall{ServiceID: "$chord.service", Member: "subscribe",
		Args: []chord.JsonValue{"sub", "svc", "bogus"}}
	if got := DecodeServiceControlCall(bad); got != nil {
		t.Fatalf("bad mode decoded: %+v", got)
	}
	bad = chord.ServiceCall{ServiceID: "$chord.service", Member: "subscribe", Args: []chord.JsonValue{"sub"}}
	if got := DecodeServiceControlCall(bad); got != nil {
		t.Fatalf("bad arity decoded: %+v", got)
	}
}

func TestParseServiceCall(t *testing.T) {
	call, err := ParseServiceCall(map[string]any{
		"serviceId": "sessions", "member": "list", "args": []any{map[string]any{"limit": float64(10)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.ServiceID != "sessions" || call.Member != "list" || len(call.Args) != 1 {
		t.Fatalf("call = %+v", call)
	}
	// With an instance address.
	call, err = ParseServiceCall(map[string]any{
		"serviceId": "sessions", "member": "get", "args": []any{},
		"instance": map[string]any{"key": "abc", "generation": float64(2)},
	})
	if err != nil || call.Instance == nil || call.Instance.Generation != 2 {
		t.Fatalf("call = %+v, %v", call, err)
	}
	// Unknown keys, bad address generation, and non-array args are rejected.
	for _, invalid := range []any{
		map[string]any{"serviceId": "s", "member": "m", "args": []any{}, "extra": true},
		map[string]any{"serviceId": "", "member": "m", "args": []any{}},
		map[string]any{"serviceId": "s", "member": "m", "args": "not an array"},
		map[string]any{"serviceId": "s", "member": "m", "args": []any{},
			"instance": map[string]any{"key": "k", "generation": float64(0)}},
		map[string]any{"serviceId": "s", "member": "m", "args": []any{},
			"instance": map[string]any{"key": "k"}},
		"not an object",
	} {
		if _, err := ParseServiceCall(invalid); err == nil {
			t.Fatalf("invalid call accepted: %v", invalid)
		}
	}
}

func TestParseServiceCatalogue(t *testing.T) {
	entries, err := ParseServiceCatalogue([]any{
		map[string]any{"serviceId": "a", "mode": "singleton"},
		map[string]any{"serviceId": "b", "mode": "keyed"},
	})
	if err != nil || len(entries) != 2 || entries[1].Mode != chord.ServiceModeKeyed {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	// Duplicate ids and bad modes are rejected.
	if _, err := ParseServiceCatalogue([]any{
		map[string]any{"serviceId": "a", "mode": "singleton"},
		map[string]any{"serviceId": "a", "mode": "singleton"},
	}); err == nil {
		t.Fatal("duplicate ids must be rejected")
	}
	if _, err := ParseServiceCatalogue([]any{map[string]any{"serviceId": "a", "mode": "nope"}}); err == nil {
		t.Fatal("bad mode must be rejected")
	}
}

func wireSnapshot() *WireServiceSubscriptionSnapshot {
	encoder := delta.NewEncoder()
	wireOps := encoder.Encode([]delta.Op{
		{Verb: delta.VerbReplace, Value: map[string]any{"count": float64(0)}},
		{Verb: delta.VerbSet, Path: delta.Path{"count"}, Value: float64(3)},
	})
	return &WireServiceSubscriptionSnapshot{
		ServiceID: "sessions",
		Mode:      chord.ServiceModeKeyed,
		Instances: []WireServiceInstanceSnapshot{{
			Instance: &chord.ServiceInstanceAddress{Key: "k1", Generation: 1},
			Members: []WireServiceMemberSnapshot{
				{Name: "list", Kind: MemberMethod},
				{Name: "state", Kind: MemberState, Sequence: 2, Ops: wireOps},
			},
		}},
	}
}

func TestParseWireSubscriptionSnapshot(t *testing.T) {
	// Render the wire snapshot as JSON values, then parse it back.
	value := map[string]any{
		"serviceId": "sessions",
		"mode":      "keyed",
		"instances": []any{map[string]any{
			"instance": map[string]any{"key": "k1", "generation": float64(1)},
			"members": []any{
				map[string]any{"name": "list", "kind": "method"},
				map[string]any{"name": "state", "kind": "state", "sequence": float64(2),
					"ops": []any{[]any{"r", map[string]any{"count": float64(0)}}}},
			},
		}},
	}
	snapshot, err := ParseWireServiceSubscriptionSnapshot(value)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ServiceID != "sessions" || len(snapshot.Instances) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	members := snapshot.Instances[0].Members
	if len(members) != 2 || members[0].Kind != MemberMethod || members[1].Sequence != 2 {
		t.Fatalf("members = %+v", members)
	}
	if len(members[1].Ops) != 1 || members[1].Ops[0].Verb != delta.VerbReplace {
		t.Fatalf("ops = %+v", members[1].Ops)
	}

	// Validation rejections.
	for _, invalid := range []any{
		map[string]any{"serviceId": "s", "mode": "keyed"},                                  // missing instances
		map[string]any{"serviceId": "s", "mode": "bogus", "instances": []any{}},            // bad mode
		map[string]any{"serviceId": "s", "mode": "keyed", "instances": []any{}, "x": true}, // unknown key
		map[string]any{"serviceId": "s", "mode": "keyed", "instances": []any{map[string]any{ // bad member kind
			"members": []any{map[string]any{"name": "x", "kind": "bogus"}}}}},
		map[string]any{"serviceId": "s", "mode": "keyed", "instances": []any{map[string]any{ // bad sequence
			"members": []any{map[string]any{"name": "x", "kind": "state", "sequence": float64(-1), "ops": []any{}}}}}},
	} {
		if _, err := ParseWireServiceSubscriptionSnapshot(invalid); err == nil {
			t.Fatalf("invalid snapshot accepted: %v", invalid)
		}
	}
}

func TestParseServiceProviderUpdates(t *testing.T) {
	// state
	update, err := ParseWireServiceProviderUpdate(map[string]any{
		"type": "state", "member": "count", "sequence": float64(3),
		"ops": []any{[]any{"s", []any{"count"}, float64(1)}},
	})
	if err != nil || update.Type != UpdateState || update.Member != "count" || update.Sequence != 3 {
		t.Fatalf("state = %+v, %v", update, err)
	}
	// unavailable
	update, err = ParseWireServiceProviderUpdate(map[string]any{"type": "unavailable"})
	if err != nil || update.Type != UpdateUnavailable {
		t.Fatalf("unavailable = %+v, %v", update, err)
	}
	// replaced with a snapshot
	update, err = ParseWireServiceProviderUpdate(map[string]any{
		"type":     "replaced",
		"snapshot": map[string]any{"members": []any{map[string]any{"name": "m", "kind": "method"}}},
	})
	if err != nil || update.Snapshot == nil || len(update.Snapshot.Members) != 1 {
		t.Fatalf("replaced = %+v, %v", update, err)
	}
	// spawned
	update, err = ParseWireServiceProviderUpdate(map[string]any{
		"type":     "spawned",
		"instance": map[string]any{"instance": map[string]any{"key": "k", "generation": float64(1)}, "members": []any{}},
	})
	if err != nil || update.SpawnedInstance == nil || update.SpawnedInstance.Instance.Key != "k" {
		t.Fatalf("spawned = %+v, %v", update, err)
	}
	// closed
	update, err = ParseWireServiceProviderUpdate(map[string]any{
		"type": "closed", "instance": map[string]any{"key": "k", "generation": float64(2)},
	})
	if err != nil || update.ClosedInstance == nil || update.ClosedInstance.Generation != 2 {
		t.Fatalf("closed = %+v, %v", update, err)
	}
	// Invalid shapes.
	for _, invalid := range []any{
		map[string]any{"type": "state", "member": "m", "sequence": float64(0), "ops": []any{}}, // sequence >= 1
		map[string]any{"type": "state", "member": "m", "sequence": float64(1), "ops": []any{}, "x": true},
		map[string]any{"type": "closed"},
		map[string]any{"type": "unknown"},
	} {
		if _, err := ParseWireServiceProviderUpdate(invalid); err == nil {
			t.Fatalf("invalid update accepted: %v", invalid)
		}
	}
}

func TestServiceStateCodecRoundTrip(t *testing.T) {
	// The decoder must mirror the encoder's registrations.
	encoder := NewServiceStateEncoder()
	decoder := NewServiceStateDecoder()

	snapshot := &ServiceSubscriptionSnapshot{
		ServiceID: "sessions", Mode: chord.ServiceModeKeyed,
		Instances: []ServiceInstanceSnapshot{{
			Instance: &chord.ServiceInstanceAddress{Key: "k1", Generation: 1},
			Members: []ServiceMemberSnapshot{
				{Name: "list", Kind: MemberMethod},
				{Name: "state", Kind: MemberState, Sequence: 1, Ops: []delta.Op{
					{Verb: delta.VerbReplace, Value: map[string]any{"count": float64(0)}},
				}},
			},
		}},
	}
	wire, err := encoder.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Instances[0].Members[1].Ops == nil {
		t.Fatal("state ops must be encoded")
	}

	decoded, err := decoder.DecodeSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Instances[0].Members[1].Ops[0].Verb != delta.VerbReplace {
		t.Fatalf("decoded ops = %+v", decoded.Instances[0].Members[1].Ops)
	}

	// A state update encodes and decodes through the same registrations.
	update := &ServiceProviderUpdate{
		Type: UpdateState, Instance: &chord.ServiceInstanceAddress{Key: "k1", Generation: 1},
		Member: "state", Sequence: 2,
		Ops: []delta.Op{{Verb: delta.VerbAppend, Path: delta.Path{"log"}, Text: "x"}},
	}
	wireUpdate, err := encoder.EncodeUpdate(update)
	if err != nil {
		t.Fatal(err)
	}
	decodedUpdate, err := decoder.DecodeUpdate(wireUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if decodedUpdate.Ops[0].Text != "x" || decodedUpdate.Sequence != 2 {
		t.Fatalf("decoded update = %+v", decodedUpdate)
	}

	// An unknown state member is an error on both sides.
	unknown := &ServiceProviderUpdate{
		Type: UpdateState, Instance: &chord.ServiceInstanceAddress{Key: "k1", Generation: 1},
		Member: "missing", Sequence: 3, Ops: nil,
	}
	if _, err := encoder.EncodeUpdate(unknown); err == nil ||
		!strings.Contains(err.Error(), "Unknown service state") {
		t.Fatalf("err = %v", err)
	}

	// A duplicate state registration within one snapshot is an error.
	duplicate := &ServiceSubscriptionSnapshot{
		ServiceID: "s", Mode: chord.ServiceModeSingleton,
		Instances: []ServiceInstanceSnapshot{{Members: []ServiceMemberSnapshot{
			{Name: "state", Kind: MemberState, Ops: []delta.Op{}},
			{Name: "state", Kind: MemberState, Ops: []delta.Op{}},
		}}},
	}
	if _, err := encoder.EncodeSnapshot(duplicate); err == nil ||
		!strings.Contains(err.Error(), "Duplicate service state") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceStateCodecResetsAndClosed(t *testing.T) {
	decoder := NewServiceStateDecoder()

	// unavailable resets the registrations, so a later state update fails.
	snapshot := &ServiceSubscriptionSnapshot{
		ServiceID: "s", Mode: chord.ServiceModeSingleton,
		Instances: []ServiceInstanceSnapshot{{Members: []ServiceMemberSnapshot{
			{Name: "state", Kind: MemberState, Ops: []delta.Op{}},
		}}},
	}
	encoder := NewServiceStateEncoder()
	wire, err := encoder.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeSnapshot(wire); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeUpdate(&WireServiceProviderUpdate{Type: UpdateUnavailable}); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeUpdate(&WireServiceProviderUpdate{
		Type: UpdateState, Member: "state", Sequence: 1,
	}); err == nil {
		t.Fatal("state update after reset must fail")
	}

	// closed removes the instance's registrations.
	instance := &chord.ServiceInstanceAddress{Key: "k", Generation: 1}
	snapshot = &ServiceSubscriptionSnapshot{
		ServiceID: "s", Mode: chord.ServiceModeSingleton,
		Instances: []ServiceInstanceSnapshot{{Instance: instance, Members: []ServiceMemberSnapshot{
			{Name: "state", Kind: MemberState, Ops: []delta.Op{}},
		}}},
	}
	wire, err = encoder.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeSnapshot(wire); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeUpdate(&WireServiceProviderUpdate{Type: UpdateClosed, ClosedInstance: instance}); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodeUpdate(&WireServiceProviderUpdate{
		Type: UpdateState, Instance: instance, Member: "state", Sequence: 1,
	}); err == nil {
		t.Fatal("state update after close must fail")
	}
}

func TestRemoteServiceErrorCodes(t *testing.T) {
	for _, code := range RemoteServiceErrorCodes {
		if !IsRemoteServiceErrorCode(code) {
			t.Fatalf("%s should be a known code", code)
		}
	}
	if IsRemoteServiceErrorCode("nope") || IsRemoteServiceErrorCode(42) {
		t.Fatal("unknown codes must be rejected")
	}
	err := &RemoteServiceError{Code: "service_not_found", Message: "no such service"}
	if err.Error() != "no such service" || err.Code != "service_not_found" {
		t.Fatalf("err = %+v", err)
	}
}

func TestParseOpTuple(t *testing.T) {
	op, err := ParseOpTuple([]any{"s", []any{"a", float64(0)}, "value"})
	if err != nil || op.Verb != delta.VerbSet || op.Path[1] != 0 || op.Value != "value" {
		t.Fatalf("op = %+v, %v", op, err)
	}
	op, err = ParseOpTuple([]any{"p", []any{"xs"}, float64(1), float64(2), []any{float64(9)}})
	if err != nil || op.Verb != delta.VerbSplice || op.Index != 1 || op.Remove != 2 || len(op.Items) != 1 {
		t.Fatalf("splice = %+v, %v", op, err)
	}
	for _, invalid := range []any{
		"nope", []any{}, []any{"x"}, []any{"s", []any{"a"}}, []any{"t", []any{"a"}, float64(-1)},
	} {
		if _, err := ParseOpTuple(invalid); err == nil {
			t.Fatalf("invalid tuple accepted: %v", invalid)
		}
	}
}
