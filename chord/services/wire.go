package services

import (
	"fmt"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/wire.ts: control calls, strict validation, and the
// wire tuple mapping.

const (
	serviceControlID         = "$chord.service"
	serviceCatalogueMember   = "catalogue"
	serviceSubscribeMember   = "subscribe"
	serviceUnsubscribeMember = "unsubscribe"
)

// ServiceControlCall is a decoded control call.
type ServiceControlCall struct {
	Type           string // "catalogue" | "subscribe" | "unsubscribe"
	SubscriptionID string
	ServiceID      string
	Mode           chord.ServiceMode
}

// CreateServiceCatalogueCall builds the catalogue control call.
func CreateServiceCatalogueCall() chord.ServiceCall {
	return chord.ServiceCall{ServiceID: serviceControlID, Member: serviceCatalogueMember, Args: []chord.JsonValue{}}
}

// CreateServiceSubscribeCall builds the subscribe control call.
func CreateServiceSubscribeCall(subscriptionID, serviceID string, mode chord.ServiceMode) chord.ServiceCall {
	return chord.ServiceCall{ServiceID: serviceControlID, Member: serviceSubscribeMember,
		Args: []chord.JsonValue{subscriptionID, serviceID, mode}}
}

// CreateServiceUnsubscribeCall builds the unsubscribe control call.
func CreateServiceUnsubscribeCall(subscriptionID string) chord.ServiceCall {
	return chord.ServiceCall{ServiceID: serviceControlID, Member: serviceUnsubscribeMember,
		Args: []chord.JsonValue{subscriptionID}}
}

// DecodeServiceControlCall decodes a control call, or nil when it is not one.
func DecodeServiceControlCall(call chord.ServiceCall) *ServiceControlCall {
	if call.ServiceID != serviceControlID || call.Instance != nil {
		return nil
	}
	switch call.Member {
	case serviceCatalogueMember:
		if len(call.Args) == 0 {
			return &ServiceControlCall{Type: "catalogue"}
		}
	case serviceSubscribeMember:
		if len(call.Args) == 3 && isID(call.Args[0]) && isID(call.Args[1]) {
			mode, ok := call.Args[2].(string)
			if ok && isMode(mode) {
				return &ServiceControlCall{Type: "subscribe",
					SubscriptionID: call.Args[0].(string), ServiceID: call.Args[1].(string), Mode: mode}
			}
		}
	case serviceUnsubscribeMember:
		if len(call.Args) == 1 && isID(call.Args[0]) {
			return &ServiceControlCall{Type: "unsubscribe", SubscriptionID: call.Args[0].(string)}
		}
	}
	return nil
}

// Validation helpers.

func isID(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func isMode(value any) bool {
	text, ok := value.(string)
	return ok && (text == chord.ServiceModeSingleton || text == chord.ServiceModeKeyed)
}

func isIntegerAtLeast(value any, minimum int) (int, bool) {
	switch typed := value.(type) {
	case int:
		if typed >= minimum {
			return typed, true
		}
	case int64:
		if int(typed) >= minimum {
			return int(typed), true
		}
	case float64:
		if typed == float64(int(typed)) && int(typed) >= minimum {
			return int(typed), true
		}
	}
	return 0, false
}

// record checks a plain JSON object.
func record(value any, description string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Invalid %s", description)
	}
	return object, nil
}

// assertKeys enforces required keys and rejects unknown ones.
func assertKeys(value map[string]any, required []string, optional []string, description string) error {
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, ok := value[key]; !ok {
			return fmt.Errorf("Invalid %s", description)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range value {
		if !allowed[key] {
			return fmt.Errorf("Invalid %s", description)
		}
	}
	return nil
}

// ParseServiceCall validates a service call.
func ParseServiceCall(value any) (chord.ServiceCall, error) {
	call, err := record(value, "service call")
	if err != nil {
		return chord.ServiceCall{}, err
	}
	if err := assertKeys(call, []string{"serviceId", "member", "args"}, []string{"instance"}, "service call"); err != nil {
		return chord.ServiceCall{}, err
	}
	if !isID(call["serviceId"]) || !isID(call["member"]) {
		return chord.ServiceCall{}, fmt.Errorf("Invalid service call")
	}
	args, ok := call["args"].([]any)
	if !ok {
		return chord.ServiceCall{}, fmt.Errorf("Invalid service call")
	}
	out := chord.ServiceCall{
		ServiceID: call["serviceId"].(string),
		Member:    call["member"].(string),
		Args:      args,
	}
	if raw, ok := call["instance"]; ok && raw != nil {
		address, err := parseAddress(raw)
		if err != nil {
			return chord.ServiceCall{}, err
		}
		out.Instance = address
	}
	return out, nil
}

func parseAddress(value any) (*chord.ServiceInstanceAddress, error) {
	address, err := record(value, "service instance address")
	if err != nil {
		return nil, err
	}
	if err := assertKeys(address, []string{"key", "generation"}, nil, "service instance address"); err != nil {
		return nil, err
	}
	if !isID(address["key"]) {
		return nil, fmt.Errorf("Invalid service instance address")
	}
	generation, ok := isIntegerAtLeast(address["generation"], 1)
	if !ok {
		return nil, fmt.Errorf("Invalid service instance address")
	}
	return &chord.ServiceInstanceAddress{Key: address["key"].(string), Generation: generation}, nil
}

// ParseServiceCatalogue validates a catalogue result.
func ParseServiceCatalogue(value any) ([]chord.ServiceCatalogueEntry, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid service catalogue")
	}
	seen := map[string]bool{}
	out := make([]chord.ServiceCatalogueEntry, 0, len(list))
	for _, candidate := range list {
		entry, err := record(candidate, "service catalogue entry")
		if err != nil {
			return nil, fmt.Errorf("Invalid service catalogue")
		}
		if err := assertKeys(entry, []string{"serviceId", "mode"}, nil, "service catalogue entry"); err != nil {
			return nil, fmt.Errorf("Invalid service catalogue")
		}
		if !isID(entry["serviceId"]) || !isMode(entry["mode"]) || seen[entry["serviceId"].(string)] {
			return nil, fmt.Errorf("Invalid service catalogue")
		}
		seen[entry["serviceId"].(string)] = true
		out = append(out, chord.ServiceCatalogueEntry{ServiceID: entry["serviceId"].(string), Mode: entry["mode"].(string)})
	}
	return out, nil
}

// ParseServiceSubscriptionSnapshot validates a decoded snapshot.
func ParseServiceSubscriptionSnapshot(value any) (*ServiceSubscriptionSnapshot, error) {
	return parseSubscriptionSnapshot(value, false)
}

// ParseWireServiceSubscriptionSnapshot validates a wire snapshot.
func ParseWireServiceSubscriptionSnapshot(value any) (*WireServiceSubscriptionSnapshot, error) {
	snapshot, err := record(value, "service subscription snapshot")
	if err != nil {
		return nil, err
	}
	if err := assertKeys(snapshot, []string{"serviceId", "mode", "instances"}, nil, "service subscription snapshot"); err != nil {
		return nil, err
	}
	if !isID(snapshot["serviceId"]) || !isMode(snapshot["mode"]) {
		return nil, fmt.Errorf("Invalid service subscription snapshot")
	}
	instancesRaw, ok := snapshot["instances"].([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid service subscription snapshot")
	}
	out := &WireServiceSubscriptionSnapshot{
		ServiceID: snapshot["serviceId"].(string),
		Mode:      snapshot["mode"].(string),
	}
	for _, raw := range instancesRaw {
		instance, err := parseWireInstance(raw)
		if err != nil {
			return nil, err
		}
		out.Instances = append(out.Instances, *instance)
	}
	return out, nil
}

func parseSubscriptionSnapshot(value any, wire bool) (*ServiceSubscriptionSnapshot, error) {
	snapshot, err := record(value, "service subscription snapshot")
	if err != nil {
		return nil, err
	}
	if err := assertKeys(snapshot, []string{"serviceId", "mode", "instances"}, nil, "service subscription snapshot"); err != nil {
		return nil, err
	}
	if !isID(snapshot["serviceId"]) || !isMode(snapshot["mode"]) {
		return nil, fmt.Errorf("Invalid service subscription snapshot")
	}
	instancesRaw, ok := snapshot["instances"].([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid service subscription snapshot")
	}
	out := &ServiceSubscriptionSnapshot{
		ServiceID: snapshot["serviceId"].(string),
		Mode:      snapshot["mode"].(string),
	}
	for _, raw := range instancesRaw {
		instance, err := parseInstance(raw)
		if err != nil {
			return nil, err
		}
		out.Instances = append(out.Instances, *instance)
	}
	return out, nil
}

func parseInstance(value any) (*ServiceInstanceSnapshot, error) {
	instance, err := record(value, "service instance snapshot")
	if err != nil {
		return nil, err
	}
	if err := assertKeys(instance, []string{"members"}, []string{"instance"}, "service instance snapshot"); err != nil {
		return nil, err
	}
	out := &ServiceInstanceSnapshot{}
	if raw, ok := instance["instance"]; ok && raw != nil {
		address, err := parseAddress(raw)
		if err != nil {
			return nil, err
		}
		out.Instance = address
	}
	membersRaw, ok := instance["members"].([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid service instance snapshot")
	}
	for _, raw := range membersRaw {
		member, err := record(raw, "service member snapshot")
		if err != nil {
			return nil, fmt.Errorf("Invalid service member snapshot")
		}
		kind, _ := member["kind"].(string)
		switch kind {
		case MemberMethod:
			if err := assertKeys(member, []string{"name", "kind"}, nil, "service method snapshot"); err != nil {
				return nil, fmt.Errorf("Invalid service method snapshot")
			}
			if !isID(member["name"]) {
				return nil, fmt.Errorf("Invalid service method snapshot")
			}
			out.Members = append(out.Members, ServiceMemberSnapshot{Name: member["name"].(string), Kind: kind})
		case MemberState:
			if err := assertKeys(member, []string{"name", "kind", "sequence", "ops"}, nil, "service state snapshot"); err != nil {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			sequence, ok := isIntegerAtLeast(member["sequence"], 0)
			if !ok || !isID(member["name"]) {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			opsRaw, ok := member["ops"].([]any)
			if !ok {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			ops := make([]delta.Op, 0, len(opsRaw))
			for _, opRaw := range opsRaw {
				op, err := ParseOpTuple(opRaw)
				if err != nil {
					return nil, err
				}
				ops = append(ops, op)
			}
			out.Members = append(out.Members, ServiceMemberSnapshot{
				Name: member["name"].(string), Kind: kind, Sequence: sequence, Ops: ops,
			})
		default:
			return nil, fmt.Errorf("Invalid service member snapshot")
		}
	}
	return out, nil
}

func parseWireInstance(value any) (*WireServiceInstanceSnapshot, error) {
	instance, err := record(value, "service instance snapshot")
	if err != nil {
		return nil, err
	}
	if err := assertKeys(instance, []string{"members"}, []string{"instance"}, "service instance snapshot"); err != nil {
		return nil, err
	}
	out := &WireServiceInstanceSnapshot{}
	if raw, ok := instance["instance"]; ok && raw != nil {
		address, err := parseAddress(raw)
		if err != nil {
			return nil, err
		}
		out.Instance = address
	}
	membersRaw, ok := instance["members"].([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid service instance snapshot")
	}
	for _, raw := range membersRaw {
		member, err := record(raw, "service member snapshot")
		if err != nil {
			return nil, fmt.Errorf("Invalid service member snapshot")
		}
		kind, _ := member["kind"].(string)
		switch kind {
		case MemberMethod:
			if err := assertKeys(member, []string{"name", "kind"}, nil, "service method snapshot"); err != nil {
				return nil, fmt.Errorf("Invalid service method snapshot")
			}
			if !isID(member["name"]) {
				return nil, fmt.Errorf("Invalid service method snapshot")
			}
			out.Members = append(out.Members, WireServiceMemberSnapshot{Name: member["name"].(string), Kind: kind})
		case MemberState:
			if err := assertKeys(member, []string{"name", "kind", "sequence", "ops"}, nil, "service state snapshot"); err != nil {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			sequence, ok := isIntegerAtLeast(member["sequence"], 0)
			if !ok || !isID(member["name"]) {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			opsRaw, ok := member["ops"].([]any)
			if !ok {
				return nil, fmt.Errorf("Invalid service state snapshot")
			}
			ops := make([]delta.WireOp, 0, len(opsRaw))
			for _, opRaw := range opsRaw {
				op, err := delta.ParseWireOp(opRaw)
				if err != nil {
					return nil, err
				}
				ops = append(ops, op)
			}
			out.Members = append(out.Members, WireServiceMemberSnapshot{
				Name: member["name"].(string), Kind: kind, Sequence: sequence, Ops: ops,
			})
		default:
			return nil, fmt.Errorf("Invalid service member snapshot")
		}
	}
	return out, nil
}

// ParseServiceProviderUpdate validates a decoded provider update.
func ParseServiceProviderUpdate(value any) (*ServiceProviderUpdate, error) {
	update, err := record(value, "service provider update")
	if err != nil {
		return nil, err
	}
	kind, _ := update["type"].(string)
	switch kind {
	case UpdateState:
		if err := assertKeys(update, []string{"type", "member", "sequence", "ops"}, []string{"instance"}, "state update"); err != nil {
			return nil, fmt.Errorf("Invalid service state update")
		}
		sequence, ok := isIntegerAtLeast(update["sequence"], 1)
		if !ok || !isID(update["member"]) {
			return nil, fmt.Errorf("Invalid service state update")
		}
		opsRaw, ok := update["ops"].([]any)
		if !ok {
			return nil, fmt.Errorf("Invalid service state update")
		}
		out := &ServiceProviderUpdate{Type: kind, Member: update["member"].(string), Sequence: sequence}
		if raw, ok := update["instance"]; ok && raw != nil {
			address, err := parseAddress(raw)
			if err != nil {
				return nil, err
			}
			out.Instance = address
		}
		for _, opRaw := range opsRaw {
			op, err := ParseOpTuple(opRaw)
			if err != nil {
				return nil, err
			}
			out.Ops = append(out.Ops, op)
		}
		return out, nil
	case UpdateUnavailable:
		if err := assertKeys(update, []string{"type"}, nil, "unavailable update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		return &ServiceProviderUpdate{Type: kind}, nil
	case UpdateReplaced:
		if err := assertKeys(update, []string{"type", "snapshot"}, nil, "replacement update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		snapshot, err := parseInstance(update["snapshot"])
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{Type: kind, Snapshot: snapshot}, nil
	case UpdateSpawned:
		if err := assertKeys(update, []string{"type", "instance"}, nil, "spawn update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		instance, err := parseInstance(update["instance"])
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{Type: kind, SpawnedInstance: instance}, nil
	case UpdateClosed:
		if err := assertKeys(update, []string{"type", "instance"}, nil, "close update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		address, err := parseAddress(update["instance"])
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{Type: kind, ClosedInstance: address}, nil
	default:
		return nil, fmt.Errorf("Invalid service provider update")
	}
}

// ParseWireServiceProviderUpdate validates a wire provider update.
func ParseWireServiceProviderUpdate(value any) (*WireServiceProviderUpdate, error) {
	update, err := record(value, "service provider update")
	if err != nil {
		return nil, err
	}
	kind, _ := update["type"].(string)
	switch kind {
	case UpdateState:
		if err := assertKeys(update, []string{"type", "member", "sequence", "ops"}, []string{"instance"}, "state update"); err != nil {
			return nil, fmt.Errorf("Invalid service state update")
		}
		sequence, ok := isIntegerAtLeast(update["sequence"], 1)
		if !ok || !isID(update["member"]) {
			return nil, fmt.Errorf("Invalid service state update")
		}
		opsRaw, ok := update["ops"].([]any)
		if !ok {
			return nil, fmt.Errorf("Invalid service state update")
		}
		out := &WireServiceProviderUpdate{Type: kind, Member: update["member"].(string), Sequence: sequence}
		if raw, ok := update["instance"]; ok && raw != nil {
			address, err := parseAddress(raw)
			if err != nil {
				return nil, err
			}
			out.Instance = address
		}
		for _, opRaw := range opsRaw {
			op, err := delta.ParseWireOp(opRaw)
			if err != nil {
				return nil, err
			}
			out.Ops = append(out.Ops, op)
		}
		return out, nil
	case UpdateUnavailable:
		if err := assertKeys(update, []string{"type"}, nil, "unavailable update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		return &WireServiceProviderUpdate{Type: kind}, nil
	case UpdateReplaced:
		if err := assertKeys(update, []string{"type", "snapshot"}, nil, "replacement update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		snapshot, err := parseWireInstance(update["snapshot"])
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{Type: kind, Snapshot: snapshot}, nil
	case UpdateSpawned:
		if err := assertKeys(update, []string{"type", "instance"}, nil, "spawn update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		instance, err := parseWireInstance(update["instance"])
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{Type: kind, SpawnedInstance: instance}, nil
	case UpdateClosed:
		if err := assertKeys(update, []string{"type", "instance"}, nil, "close update"); err != nil {
			return nil, fmt.Errorf("Invalid service provider update")
		}
		address, err := parseAddress(update["instance"])
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{Type: kind, ClosedInstance: address}, nil
	default:
		return nil, fmt.Errorf("Invalid service provider update")
	}
}

// ParseOpTuple converts a JSON tuple into a decoded op.
func ParseOpTuple(value any) (delta.Op, error) {
	tuple, ok := value.([]any)
	if !ok || len(tuple) == 0 {
		return delta.Op{}, fmt.Errorf("op is not a tuple")
	}
	verb, ok := tuple[0].(string)
	if !ok {
		return delta.Op{}, fmt.Errorf("op verb is not a string")
	}
	op := delta.Op{Verb: verb}
	pathArg := func(raw any) (delta.Path, error) {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("path is not an array")
		}
		path := make(delta.Path, 0, len(list))
		for _, segment := range list {
			switch typed := segment.(type) {
			case string:
				path = append(path, typed)
			case int:
				path = append(path, typed)
			case int64:
				path = append(path, int(typed))
			case float64:
				if typed == float64(int(typed)) {
					path = append(path, int(typed))
					continue
				}
				return nil, fmt.Errorf("bad path segment")
			default:
				return nil, fmt.Errorf("bad path segment")
			}
		}
		return path, nil
	}
	switch verb {
	case delta.VerbReplace:
		if len(tuple) != 2 {
			return delta.Op{}, fmt.Errorf("r arity")
		}
		op.Value = tuple[1]
	case delta.VerbSet:
		if len(tuple) != 3 {
			return delta.Op{}, fmt.Errorf("s arity")
		}
		path, err := pathArg(tuple[1])
		if err != nil {
			return delta.Op{}, err
		}
		op.Path, op.Value = path, tuple[2]
	case delta.VerbDelete:
		if len(tuple) != 2 {
			return delta.Op{}, fmt.Errorf("d arity")
		}
		path, err := pathArg(tuple[1])
		if err != nil {
			return delta.Op{}, err
		}
		op.Path = path
	case delta.VerbAppend:
		if len(tuple) != 3 {
			return delta.Op{}, fmt.Errorf("a arity")
		}
		path, err := pathArg(tuple[1])
		if err != nil {
			return delta.Op{}, err
		}
		text, ok := tuple[2].(string)
		if !ok {
			return delta.Op{}, fmt.Errorf("a value")
		}
		op.Path, op.Text = path, text
	case delta.VerbTruncate:
		if len(tuple) != 3 {
			return delta.Op{}, fmt.Errorf("t arity")
		}
		path, err := pathArg(tuple[1])
		if err != nil {
			return delta.Op{}, err
		}
		count, ok := isIntegerAtLeast(tuple[2], 0)
		if !ok {
			return delta.Op{}, fmt.Errorf("t count")
		}
		op.Path, op.Count = path, count
	case delta.VerbSplice:
		if len(tuple) != 5 {
			return delta.Op{}, fmt.Errorf("p arity")
		}
		path, err := pathArg(tuple[1])
		if err != nil {
			return delta.Op{}, err
		}
		index, ok := isIntegerAtLeast(tuple[2], 0)
		if !ok {
			return delta.Op{}, fmt.Errorf("p index")
		}
		remove, ok := isIntegerAtLeast(tuple[3], 0)
		if !ok {
			return delta.Op{}, fmt.Errorf("p remove")
		}
		items, ok := tuple[4].([]any)
		if !ok {
			return delta.Op{}, fmt.Errorf("p items")
		}
		op.Path, op.Index, op.Remove = path, index, remove
		op.Items = make([]chord.JsonValue, len(items))
		copy(op.Items, items)
	default:
		return delta.Op{}, fmt.Errorf("unknown op verb: %v", verb)
	}
	if err := delta.AssertValidOp(op); err != nil {
		return delta.Op{}, err
	}
	return op, nil
}
