package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelInputLimitsParseAndRoundTrip pins the inputLimits shape against
// upstream ModelInputLimitsSchema: the key sits between input and cost, the
// optional numbers disappear when unset, and a user models.json override that
// sets them survives a round trip byte-for-byte — catalogue parsing is
// byte-parity territory.
func TestModelInputLimitsParseAndRoundTrip(t *testing.T) {
	const source = `{"id":"m","name":"M","api":"anthropic-messages","provider":"anthropic","reasoning":false,"input":["text","image"],"inputLimits":{"maxRequestBytes":10485760,"images":{"resize":{"maxWidth":1024,"maxHeight":768,"maxBytes":2000000,"jpegQuality":70},"maxPerMessage":4,"maxPerRequest":12}},"cost":{"input":1,"output":2},"contextWindow":1000,"maxTokens":100}`

	var model Model
	if err := json.Unmarshal([]byte(source), &model); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if model.InputLimits == nil || model.InputLimits.Images == nil || model.InputLimits.Images.Resize == nil {
		t.Fatalf("inputLimits did not parse: %#v", model.InputLimits)
	}
	if model.InputLimits.MaxRequestBytes != 10485760 {
		t.Errorf("maxRequestBytes = %d", model.InputLimits.MaxRequestBytes)
	}
	resize := model.InputLimits.Images.Resize
	if resize.MaxWidth != 1024 || resize.MaxHeight != 768 || resize.MaxBytes != 2000000 || resize.JPEGQuality != 70 {
		t.Errorf("resize = %#v", resize)
	}
	if model.InputLimits.Images.MaxPerMessage != 4 || model.InputLimits.Images.MaxPerRequest != 12 {
		t.Errorf("per-message/request = %#v", model.InputLimits.Images)
	}

	encoded, err := json.Marshal(&model)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Key order is the byte-parity property that matters: inputLimits sits between
	// input and cost, as it does in upstream's ModelDefinitionSchema.
	text := string(encoded)
	inputAt := strings.Index(text, `"input":`)
	limitsAt := strings.Index(text, `"inputLimits":`)
	costAt := strings.Index(text, `"cost":`)
	if inputAt < 0 || limitsAt < 0 || costAt < 0 || !(inputAt < limitsAt && limitsAt < costAt) {
		t.Fatalf("inputLimits is not between input and cost: %s", text)
	}
	// The encoded form is stable: a second round trip changes nothing.
	var again Model
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	second, err := json.Marshal(&again)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != text {
		t.Errorf("round trip is not stable:\n first %s\nsecond %s", text, second)
	}
}

// TestModelInputLimitsOmittedWhenUnset keeps models without the key identical to
// the catalogue's own JSON.
func TestModelInputLimitsOmittedWhenUnset(t *testing.T) {
	const source = `{"id":"m","name":"M","api":"anthropic-messages","provider":"anthropic","reasoning":false,"input":["text"],"cost":{"input":1,"output":2},"contextWindow":1000,"maxTokens":100}`
	var model Model
	if err := json.Unmarshal([]byte(source), &model); err != nil {
		t.Fatal(err)
	}
	if model.InputLimits != nil {
		t.Fatalf("inputLimits = %#v, want nil", model.InputLimits)
	}
	encoded, err := json.Marshal(&model)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "inputLimits") {
		t.Errorf("an unset inputLimits was emitted: %s", encoded)
	}
}
