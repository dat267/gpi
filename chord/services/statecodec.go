package services

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/delta"
)

// Port of src/services/state-codec.ts: stateful operation codecs for every
// replicated state in one service subscription.

// codecRegistry maps (instance, member) to its stateful codec.
type codecRegistry[C any] struct {
	create  func() C
	entries map[string]*codecEntry[C]
	order   []string
}

type codecEntry[C any] struct {
	instance *chord.ServiceInstanceAddress
	codec    C
}

func newCodecRegistry[C any](create func() C) *codecRegistry[C] {
	return &codecRegistry[C]{create: create, entries: map[string]*codecEntry[C]{}}
}

func (r *codecRegistry[C]) reset() {
	r.entries = map[string]*codecEntry[C]{}
	r.order = nil
}

func (r *codecRegistry[C]) add(instance *chord.ServiceInstanceAddress, member string) (C, error) {
	key := stateKey(instance, member)
	if _, exists := r.entries[key]; exists {
		var zero C
		return zero, fmt.Errorf("Duplicate service state %s", describeState(instance, member))
	}
	codec := r.create()
	r.entries[key] = &codecEntry[C]{instance: instance, codec: codec}
	r.order = append(r.order, key)
	return codec, nil
}

func (r *codecRegistry[C]) get(instance *chord.ServiceInstanceAddress, member string) (C, error) {
	entry, ok := r.entries[stateKey(instance, member)]
	if !ok {
		var zero C
		return zero, fmt.Errorf("Unknown service state %s", describeState(instance, member))
	}
	return entry.codec, nil
}

func (r *codecRegistry[C]) removeInstance(instance *chord.ServiceInstanceAddress) {
	for key, entry := range r.entries {
		if sameAddress(entry.instance, instance) {
			delete(r.entries, key)
		}
	}
	var kept []string
	for _, key := range r.order {
		if _, ok := r.entries[key]; ok {
			kept = append(kept, key)
		}
	}
	r.order = kept
}

func stateKey(instance *chord.ServiceInstanceAddress, member string) string {
	key := "null"
	generation := "null"
	if instance != nil {
		key = instance.Key
		generation = fmt.Sprintf("%d", instance.Generation)
	}
	return strings.Join([]string{key, generation, member}, "\x00")
}

func sameAddress(left, right *chord.ServiceInstanceAddress) bool {
	if right == nil {
		return left == nil
	}
	return left != nil && left.Key == right.Key && left.Generation == right.Generation
}

func describeState(instance *chord.ServiceInstanceAddress, member string) string {
	if instance == nil {
		return member
	}
	return fmt.Sprintf("%s@%d.%s", instance.Key, instance.Generation, member)
}

// serviceStateEncoder implements ServiceStateEncoder.
type serviceStateEncoder struct {
	codecs *codecRegistry[*delta.Encoder]
}

// NewServiceStateEncoder builds a per-subscription encoder.
func NewServiceStateEncoder() ServiceStateEncoder {
	return &serviceStateEncoder{codecs: newCodecRegistry(delta.NewEncoder)}
}

func (e *serviceStateEncoder) EncodeSnapshot(snapshot *ServiceSubscriptionSnapshot) (*WireServiceSubscriptionSnapshot, error) {
	e.codecs.reset()
	out := &WireServiceSubscriptionSnapshot{ServiceID: snapshot.ServiceID, Mode: snapshot.Mode}
	for _, instance := range snapshot.Instances {
		encoded, err := e.encodeInstance(&instance)
		if err != nil {
			return nil, err
		}
		out.Instances = append(out.Instances, *encoded)
	}
	return out, nil
}

func (e *serviceStateEncoder) EncodeUpdate(update *ServiceProviderUpdate) (*WireServiceProviderUpdate, error) {
	switch update.Type {
	case UpdateState:
		codec, err := e.codecs.get(update.Instance, update.Member)
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{
			Type: update.Type, Instance: update.Instance, Member: update.Member,
			Sequence: update.Sequence, Ops: codec.Encode(update.Ops),
		}, nil
	case UpdateReplaced:
		e.codecs.reset()
		snapshot, err := e.encodeInstance(update.Snapshot)
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{Type: update.Type, Snapshot: snapshot}, nil
	case UpdateSpawned:
		instance, err := e.encodeInstance(update.SpawnedInstance)
		if err != nil {
			return nil, err
		}
		return &WireServiceProviderUpdate{Type: update.Type, SpawnedInstance: instance}, nil
	case UpdateUnavailable:
		e.codecs.reset()
		return &WireServiceProviderUpdate{Type: update.Type}, nil
	case UpdateClosed:
		e.codecs.removeInstance(update.ClosedInstance)
		return &WireServiceProviderUpdate{Type: update.Type, ClosedInstance: update.ClosedInstance}, nil
	default:
		return nil, fmt.Errorf("Invalid service provider update")
	}
}

func (e *serviceStateEncoder) encodeInstance(instance *ServiceInstanceSnapshot) (*WireServiceInstanceSnapshot, error) {
	out := &WireServiceInstanceSnapshot{Instance: instance.Instance}
	for _, member := range instance.Members {
		if member.Kind != MemberState {
			out.Members = append(out.Members, WireServiceMemberSnapshot{Name: member.Name, Kind: member.Kind})
			continue
		}
		codec, err := e.codecs.add(instance.Instance, member.Name)
		if err != nil {
			return nil, err
		}
		out.Members = append(out.Members, WireServiceMemberSnapshot{
			Name: member.Name, Kind: member.Kind, Sequence: member.Sequence, Ops: codec.Encode(member.Ops),
		})
	}
	return out, nil
}

// serviceStateDecoder implements ServiceStateDecoder.
type serviceStateDecoder struct {
	codecs *codecRegistry[*delta.Decoder]
}

// NewServiceStateDecoder builds a per-subscription decoder.
func NewServiceStateDecoder() ServiceStateDecoder {
	return &serviceStateDecoder{codecs: newCodecRegistry(delta.NewDecoder)}
}

func (d *serviceStateDecoder) DecodeSnapshot(snapshot *WireServiceSubscriptionSnapshot) (*ServiceSubscriptionSnapshot, error) {
	d.codecs.reset()
	out := &ServiceSubscriptionSnapshot{ServiceID: snapshot.ServiceID, Mode: snapshot.Mode}
	for _, instance := range snapshot.Instances {
		decoded, err := d.decodeInstance(&instance)
		if err != nil {
			return nil, err
		}
		out.Instances = append(out.Instances, *decoded)
	}
	return out, nil
}

func (d *serviceStateDecoder) DecodeUpdate(update *WireServiceProviderUpdate) (*ServiceProviderUpdate, error) {
	switch update.Type {
	case UpdateState:
		codec, err := d.codecs.get(update.Instance, update.Member)
		if err != nil {
			return nil, err
		}
		ops, err := codec.Decode(update.Ops)
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{
			Type: update.Type, Instance: update.Instance, Member: update.Member,
			Sequence: update.Sequence, Ops: ops,
		}, nil
	case UpdateReplaced:
		d.codecs.reset()
		snapshot, err := d.decodeInstance(update.Snapshot)
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{Type: update.Type, Snapshot: snapshot}, nil
	case UpdateSpawned:
		instance, err := d.decodeInstance(update.SpawnedInstance)
		if err != nil {
			return nil, err
		}
		return &ServiceProviderUpdate{Type: update.Type, SpawnedInstance: instance}, nil
	case UpdateUnavailable:
		d.codecs.reset()
		return &ServiceProviderUpdate{Type: update.Type}, nil
	case UpdateClosed:
		d.codecs.removeInstance(update.ClosedInstance)
		return &ServiceProviderUpdate{Type: update.Type, ClosedInstance: update.ClosedInstance}, nil
	default:
		return nil, fmt.Errorf("Invalid service provider update")
	}
}

func (d *serviceStateDecoder) decodeInstance(instance *WireServiceInstanceSnapshot) (*ServiceInstanceSnapshot, error) {
	out := &ServiceInstanceSnapshot{Instance: instance.Instance}
	for _, member := range instance.Members {
		if member.Kind != MemberState {
			out.Members = append(out.Members, ServiceMemberSnapshot{Name: member.Name, Kind: member.Kind})
			continue
		}
		codec, err := d.codecs.add(instance.Instance, member.Name)
		if err != nil {
			return nil, err
		}
		ops, err := codec.Decode(member.Ops)
		if err != nil {
			return nil, err
		}
		out.Members = append(out.Members, ServiceMemberSnapshot{
			Name: member.Name, Kind: member.Kind, Sequence: member.Sequence, Ops: ops,
		})
	}
	return out, nil
}
