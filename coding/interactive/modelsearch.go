package interactive

// Port of src/modes/interactive/model-search.ts: the search text used by the
// model selectors.

// ModelSearchItem is a model entry for search text generation.
type ModelSearchItem struct {
	ID       string
	Provider string
	Name     string
	HasName  bool
}

// GetModelSearchText builds the default search text.
func GetModelSearchText(item ModelSearchItem) string {
	name := ""
	if item.HasName {
		name = " " + item.Name
	}
	return item.ID + " " + item.Provider + " " + item.Provider + "/" + item.ID + " " + item.Provider + " " + item.ID + name
}

// GetModelSelectorSearchText builds the selector search text: the /model
// selector search should rank exact provider-prefixed queries before
// proxy-provider IDs like openrouter/openai/gpt-5, so the bare model ID is
// kept out of the leading position.
func GetModelSelectorSearchText(item ModelSearchItem) string {
	name := ""
	if item.HasName {
		name = " " + item.Name
	}
	return item.Provider + " " + item.Provider + "/" + item.ID + " " + item.Provider + " " + item.ID + name
}
