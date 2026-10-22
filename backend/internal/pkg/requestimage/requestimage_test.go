package requestimage

import (
	"fmt"
	"testing"
)

func TestHasImage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"messages base64", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"abc"}}]}]}`, true},
		{"messages url history", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]},{"role":"user","content":"again"}]}`, true},
		{"chat url", `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`, true},
		{"responses file", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-1"}]}]}`, true},
		{"responses url", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`, true},
		{"anthropic tool result", `{"messages":[{"content":[{"type":"tool_result","content":[{"type":"image","source":{"data":"abc"}}]}]}]}`, true},
		{"responses tool result", `{"input":[{"type":"function_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}]}`, true},
		{"gemini inline", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}]}`, true},
		{"gemini file", `{"contents":[{"parts":[{"file_data":{"mime_type":"image/jpeg","file_uri":"gs://image"}}]}]}`, true},
		{"text", `{"messages":[{"content":"look at an image"}],"input":"image_url"}`, false},
		{"tool schema", `{"tools":[{"type":"image","source":{"data":"abc"}}],"metadata":{"content":[{"type":"input_image","image_url":"url"}]}}`, false},
		{"text nested json", `{"input":[{"content":[{"type":"input_text","text":"{\"type\":\"image\"}"}]}]}`, false},
		{"audio", `{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"abc"}}]}]}`, false},
		{"empty image", `{"input":[{"content":[{"type":"input_image"}]}]}`, false},
		{"empty data url", `{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,"}]}]}`, false},
		{"blank data url", `{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,   "}]}]}`, false},
		{"empty chat data url", `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]}]}`, false},
		{"invalid", `{"messages":[`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasImage([]byte(tc.body)); got != tc.want {
				t.Fatalf("HasImage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasImageResponsesToolOutputShapes(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output", "tool_search_output", "tool_search_call_output", "mcp_tool_call_output"} {
		for _, output := range []string{
			`[{"type":"input_image","image_url":"data:image/png;base64,abc"}]`,
			`{"type":"input_image","image_url":"data:image/png;base64,abc"}`,
			`{"content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
			`"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,abc\"}]"`,
			`"data:image/png;base64,abc"`,
		} {
			t.Run(kind+"/"+output, func(t *testing.T) {
				if !HasImage([]byte(fmt.Sprintf(`{"input":[{"type":%q,"output":%s}]}`, kind, output))) {
					t.Fatal("supported tool output image was not detected")
				}
			})
		}
	}
	for _, output := range []string{
		`"The screenshot is at https://example.com/a.png"`,
		`{"metadata":{"type":"input_image","image_url":"url"}}`,
		`{"type":"input_image","image_url":{}}`,
		`"data:image/png;base64,"`,
		`"data:image/png;base64,   "`,
	} {
		if HasImage([]byte(`{"input":[{"type":"function_call_output","output":` + output + `}]}`)) {
			t.Fatalf("non-image output detected: %s", output)
		}
	}
}
