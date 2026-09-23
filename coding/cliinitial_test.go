package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

func TestBuildInitialPrompt(t *testing.T) {
	images := []ai.ImageContent{{Data: "AAAA", MimeType: "image/png"}}
	cases := []struct {
		name     string
		messages []string
		fileText string
		images   []ai.ImageContent
		want     InitialPrompt
	}{
		{
			name: "nothing at all",
			want: InitialPrompt{},
		},
		{
			name:     "file text only",
			fileText: "contents of README.md\n",
			want:     InitialPrompt{Message: "contents of README.md\n"},
		},
		{
			name:     "file text and a question, as one prompt",
			messages: []string{"explain this"},
			fileText: "contents of README.md\n",
			want:     InitialPrompt{Message: "contents of README.md\nexplain this"},
		},
		{
			name:     "the message alone still becomes the first prompt",
			messages: []string{"hello"},
			want:     InitialPrompt{Message: "hello"},
		},
		{
			name:     "later messages stay queued",
			messages: []string{"first", "second", "third"},
			want:     InitialPrompt{Message: "first", Rest: []string{"second", "third"}},
		},
		{
			name:     "file text, a question, and a queued follow-up",
			messages: []string{"explain this", "now summarise it"},
			fileText: "contents\n",
			want:     InitialPrompt{Message: "contents\nexplain this", Rest: []string{"now summarise it"}},
		},
		{
			name:     "images travel with the message",
			messages: []string{"what is this"},
			images:   images,
			want:     InitialPrompt{Message: "what is this", Images: images},
		},
		{
			name:     "an empty first message is still the prompt slot",
			messages: []string{"", "second"},
			want:     InitialPrompt{Rest: []string{"second"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildInitialPrompt(tc.messages, tc.fileText, tc.images)
			if got.Message != tc.want.Message {
				t.Errorf("Message = %q, want %q", got.Message, tc.want.Message)
			}
			if len(got.Rest) != len(tc.want.Rest) {
				t.Fatalf("Rest = %#v, want %#v", got.Rest, tc.want.Rest)
			}
			for i := range got.Rest {
				if got.Rest[i] != tc.want.Rest[i] {
					t.Errorf("Rest[%d] = %q, want %q", i, got.Rest[i], tc.want.Rest[i])
				}
			}
			if len(got.Images) != len(tc.want.Images) {
				t.Errorf("Images = %#v, want %#v", got.Images, tc.want.Images)
			}
		})
	}
}

// The queued follow-ups must not alias the caller's slice, or a caller that
// keeps using args.Messages would see its first element silently disappear.
func TestBuildInitialPromptDoesNotMutateMessages(t *testing.T) {
	messages := []string{"first", "second"}
	_ = BuildInitialPrompt(messages, "", nil)
	if messages[0] != "first" || len(messages) != 2 {
		t.Errorf("messages were modified: %#v", messages)
	}
}
