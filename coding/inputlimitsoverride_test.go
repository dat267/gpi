package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// TestMergeInputLimitsKeepsUnsetFields pins upstream mergeInputLimits: the spread
// is deep across inputLimits, images and images.resize, so an override that sets
// one key keeps the base model's other limits.
func TestMergeInputLimitsKeepsUnsetFields(t *testing.T) {
	base := &ai.ModelInputLimits{
		MaxRequestBytes: 10_000_000,
		Images: &ai.ModelImageLimits{
			MaxPerMessage: 4,
			MaxPerRequest: 12,
			Resize:        &ai.ModelImageResize{MaxWidth: 1568, MaxHeight: 1568, MaxBytes: 4_500_000, JPEGQuality: 80},
		},
	}

	t.Run("nil override keeps the base", func(t *testing.T) {
		got := mergeInputLimits(base, nil)
		if got != base {
			t.Fatalf("got %#v, want the base pointer", got)
		}
	})

	t.Run("one resize key keeps the rest", func(t *testing.T) {
		got := mergeInputLimits(base, &ai.ModelInputLimits{
			Images: &ai.ModelImageLimits{Resize: &ai.ModelImageResize{MaxBytes: 1_000_000}},
		})
		if got.MaxRequestBytes != 10_000_000 {
			t.Errorf("maxRequestBytes = %d, want the base value", got.MaxRequestBytes)
		}
		if got.Images.MaxPerMessage != 4 || got.Images.MaxPerRequest != 12 {
			t.Errorf("counters = %d/%d, want the base values", got.Images.MaxPerMessage, got.Images.MaxPerRequest)
		}
		resize := got.Images.Resize
		if resize.MaxBytes != 1_000_000 {
			t.Errorf("maxBytes = %d, want the override", resize.MaxBytes)
		}
		if resize.MaxWidth != 1568 || resize.MaxHeight != 1568 || resize.JPEGQuality != 80 {
			t.Errorf("resize = %+v, want the base's remaining fields", resize)
		}
	})

	t.Run("override over no base", func(t *testing.T) {
		got := mergeInputLimits(nil, &ai.ModelInputLimits{
			Images: &ai.ModelImageLimits{Resize: &ai.ModelImageResize{MaxWidth: 1024}},
		})
		if got == nil || got.Images == nil || got.Images.Resize == nil || got.Images.Resize.MaxWidth != 1024 {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("the base is not mutated", func(t *testing.T) {
		copyOfBase := *base
		copyOfResize := *base.Images.Resize
		mergeInputLimits(base, &ai.ModelInputLimits{MaxRequestBytes: 1, Images: &ai.ModelImageLimits{
			Resize: &ai.ModelImageResize{MaxWidth: 8, MaxHeight: 8, MaxBytes: 8, JPEGQuality: 8},
		}})
		if *base != copyOfBase || *base.Images.Resize != copyOfResize {
			t.Fatalf("the base was mutated: %#v", base)
		}
	})
}

// TestApplyModelOverrideInputLimits covers the override as it arrives from
// models.json: the whole block replaces nothing, it merges.
func TestApplyModelOverrideInputLimits(t *testing.T) {
	model := &ai.Model{ID: "m", InputLimits: &ai.ModelInputLimits{
		MaxRequestBytes: 100,
		Images:          &ai.ModelImageLimits{Resize: &ai.ModelImageResize{MaxWidth: 2000, JPEGQuality: 80}},
	}}
	override := ModelsJSONModelOverride{InputLimits: &ai.ModelInputLimits{
		Images: &ai.ModelImageLimits{Resize: &ai.ModelImageResize{JPEGQuality: 70}},
	}}
	updated, err := applyModelOverride(model, override)
	if err != nil {
		t.Fatal(err)
	}
	resize := updated.InputLimits.Images.Resize
	if resize.JPEGQuality != 70 || resize.MaxWidth != 2000 || updated.InputLimits.MaxRequestBytes != 100 {
		t.Fatalf("inputLimits = %#v", updated.InputLimits)
	}
	if model.InputLimits.Images.Resize.JPEGQuality != 80 {
		t.Error("the original model was mutated")
	}
}
