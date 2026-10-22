// Package requestimage detects image inputs without decoding image data.
package requestimage

import (
	"strings"

	"github.com/tidwall/gjson"
)

// HasImage inspects submitted messages, including history and structured tool
// results. Text, tool definitions and metadata are deliberately not traversed.
func HasImage(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	for _, key := range []string{"messages", "input", "contents"} {
		found := false
		items := root.Get(key)
		if !items.IsArray() {
			continue
		}
		items.ForEach(func(_, item gjson.Result) bool {
			found = contentHasImage(item.Get("content")) || geminiPartsHaveImage(item.Get("parts"))
			switch item.Get("type").String() {
			case "function_call_output", "custom_tool_call_output", "tool_search_output", "tool_search_call_output", "mcp_tool_call_output":
				found = found || toolOutputHasImage(item.Get("output"))
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func contentHasImage(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "image":
			found = block.Get("source.data").String() != "" || block.Get("source.url").String() != ""
		case "input_image":
			found = imageURLPresent(block.Get("image_url")) || block.Get("file_id").String() != ""
		case "image_url":
			found = imageURLPresent(block.Get("image_url"))
		case "tool_result":
			found = contentHasImage(block.Get("content"))
		}
		return !found
	})
	return found
}

func imageURLPresent(value gjson.Result) bool {
	if value.IsObject() {
		value = value.Get("url")
	}
	return value.Type == gjson.String && imageURLHasData(value.String())
}

func imageURLHasData(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data:image/") {
		mime, data, ok := strings.Cut(value[len("data:image/"):], ";base64,")
		return ok && mime != "" && strings.TrimSpace(data) != ""
	}
	return value != ""
}

func toolOutputHasImage(output gjson.Result) bool {
	// Match the existing Responses media bridge's JSON-string and data URL
	// forms, only inside tool outputs, never in ordinary message text.
	if output.Type == gjson.String {
		value := output.String()
		if strings.HasPrefix(value, "data:image/") {
			if imageURLHasData(value) {
				return true
			}
		}
		if !gjson.Valid(value) {
			return false
		}
		output = gjson.Parse(value)
	}
	return structuredToolOutputHasImage(output)
}

func structuredToolOutputHasImage(output gjson.Result) bool {
	if output.IsArray() {
		found := false
		output.ForEach(func(_, item gjson.Result) bool {
			found = structuredToolOutputHasImage(item)
			return !found
		})
		return found
	}
	if output.IsObject() {
		switch output.Get("type").String() {
		case "input_image", "image_url":
			if imageURLPresent(output.Get("image_url")) {
				return true
			}
		}
		return structuredToolOutputHasImage(output.Get("content"))
	}
	return false
}

func geminiPartsHaveImage(parts gjson.Result) bool {
	if !parts.IsArray() {
		return false
	}
	found := false
	parts.ForEach(func(_, part gjson.Result) bool {
		for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
			data := part.Get(key)
			mime := data.Get("mimeType").String()
			if mime == "" {
				mime = data.Get("mime_type").String()
			}
			if strings.HasPrefix(strings.ToLower(mime), "image/") &&
				(data.Get("data").String() != "" || data.Get("fileUri").String() != "" || data.Get("file_uri").String() != "") {
				found = true
				break
			}
		}
		return !found
	})
	return found
}
