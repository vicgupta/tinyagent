package model

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Image is one image attached to a tool result (opt-in --images) or to a
// user task message (--image). Data is
// the base64 encoding of the raw bytes. Images travel only in provider
// requests and trajectories; every text channel (progress log, --json, the
// SQLite log, hooks) sees Placeholder instead.
type Image struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
	// Label names the source for elision notes: a workspace path or an MCP
	// tool name.
	Label string `json:"label,omitempty"`
}

// ImageMIMETypes are the formats tinyagent sends to providers.
var ImageMIMETypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// NewImage encodes raw image bytes.
func NewImage(mime, label string, raw []byte) Image {
	return Image{MIMEType: mime, Label: label, Data: base64.StdEncoding.EncodeToString(raw)}
}

// Size returns the decoded size in bytes.
func (im Image) Size() int { return base64.StdEncoding.DecodedLen(len(im.Data)) - padding(im.Data) }

func padding(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && n < 2 && s[i] == '='; i-- {
		n++
	}
	return n
}

// DataURL returns the image as a data: URL.
func (im Image) DataURL() string { return "data:" + im.MIMEType + ";base64," + im.Data }

// Placeholder is the text form shown in place of the image, e.g.
// "[image: image/png, 48,213 bytes, sha256:ab12cd34ef56]". It never contains
// image data.
func (im Image) Placeholder() string {
	raw, _ := base64.StdEncoding.DecodeString(im.Data)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("[image: %s, %s bytes, sha256:%x]", im.MIMEType, FormatBytes(len(raw)), sum[:6])
}

// ImageTokenEstimate is the fixed token estimate for one image in context
// accounting, instead of base64 length / 4. It is close to the cost of a
// ~1,000x1,000 pixel image on Anthropic and OpenAI.
const ImageTokenEstimate = 1600

// FormatBytes adds thousands separators: 48213 -> 48,213.
func FormatBytes(n int) string {
	s := strconv.Itoa(n)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}

// chatImageNote is appended to a tool message's text when the Chat
// Completions transport (openai, openrouter, ollama) can't carry its images.
// Images are deliberately not re-sent as a synthetic user message.
const chatImageNote = "[image not shown: this provider's Chat Completions API cannot carry images in tool results, so the image above was not sent to the model; use the anthropic or codex provider to view images]"

// MaxTaskImages caps how many --image attachments one task message carries.
const MaxTaskImages = 10

// AttachmentLabels is the text added to a user message for its task
// attachments: a blank line, then one "[attached image N: name, mime, bytes]"
// line per image. It is "" without images, so image-free messages are
// unchanged.
func AttachmentLabels(images []Image) string {
	if len(images) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	for i, im := range images {
		fmt.Fprintf(&b, "\n[attached image %d: %s, %s, %d bytes]", i+1, im.Label, im.MIMEType, im.Size())
	}
	return b.String()
}

// AttachmentInfo is an attachment's metadata for events and logs: index,
// base name, MIME type, size, and sha256. It never contains image data.
func AttachmentInfo(images []Image) []map[string]any {
	out := make([]map[string]any, len(images))
	for i, im := range images {
		raw, _ := base64.StdEncoding.DecodeString(im.Data)
		out[i] = map[string]any{"index": i + 1, "name": im.Label, "mime_type": im.MIMEType, "bytes": len(raw), "sha256": fmt.Sprintf("%x", sha256.Sum256(raw))}
	}
	return out
}
