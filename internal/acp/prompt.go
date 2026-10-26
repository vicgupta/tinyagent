package acp

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// ContentBlock is an ACP content block. Only the fields tinyagent reads
// are decoded.
type ContentBlock struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	MimeType string    `json:"mimeType,omitempty"`
	Data     string    `json:"data,omitempty"`
	URI      string    `json:"uri,omitempty"`
	Name     string    `json:"name,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
}

// Resource is the resource of an embedded-context block: text or a
// base64 blob.
type Resource struct {
	URI      string  `json:"uri"`
	MimeType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
	Blob     string  `json:"blob,omitempty"`
}

// TextBlock is a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

// Prompt is a session/prompt's content: the task text and the attached
// images, in order.
type Prompt struct {
	Text   string
	Images []Image
}

// Image is one image block, decoded. MimeType is what the client claimed.
type Image struct {
	MimeType string
	Data     []byte
	Label    string
}

// ParsePrompt turns prompt blocks into task text and images. Text blocks
// are joined; a text resource becomes quoted context naming its URI; a
// resource link becomes a reference line (it is not fetched). Images are
// decoded. Other blocks (audio, binary resources) and a prompt without any
// text are invalid params.
func ParsePrompt(blocks []ContentBlock) (Prompt, error) {
	var p Prompt
	var parts []string
	for i, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		case "resource_link":
			if b.URI == "" {
				return Prompt{}, InvalidParams("prompt block %d: resource_link without a uri", i)
			}
			name := b.Name
			if name == "" {
				name = path.Base(b.URI)
			}
			parts = append(parts, fmt.Sprintf("Reference: %s (%s)", name, b.URI))
		case "resource":
			r := b.Resource
			if r == nil || r.Text == nil {
				return Prompt{}, InvalidParams("prompt block %d: only text resources are supported, not binary ones", i)
			}
			parts = append(parts, quoteContext(r.URI, *r.Text))
		case "image":
			data, err := base64.StdEncoding.DecodeString(b.Data)
			if err != nil || len(data) == 0 {
				return Prompt{}, InvalidParams("prompt block %d: image data is not valid base64", i)
			}
			label := fmt.Sprintf("image %d", len(p.Images)+1)
			if b.URI != "" {
				label = path.Base(b.URI)
			}
			p.Images = append(p.Images, Image{MimeType: b.MimeType, Data: data, Label: label})
		default:
			return Prompt{}, InvalidParams("prompt block %d: unsupported content type %q (tinyagent accepts text, image, resource and resource_link)", i, b.Type)
		}
	}
	p.Text = strings.Join(parts, "\n\n")
	if p.Text == "" {
		return Prompt{}, InvalidParams("prompt has no text")
	}
	return p, nil
}

// quoteContext fences text under a line naming uri, with a fence longer
// than any backtick run inside it.
func quoteContext(uri, text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fmt.Sprintf("Context from %s:\n%s\n%s\n%s", uri, fence, strings.TrimSuffix(text, "\n"), fence)
}
