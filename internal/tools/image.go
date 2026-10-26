package tools

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/model"
)

// ViewImageToolName is the opt-in image tool (--images).
const ViewImageToolName = "view_image"

// DefaultMaxImageBytes caps one image's raw size when max_image_bytes is unset.
const DefaultMaxImageBytes = 5 << 20

// ViewImageSpec returns the view_image definition.
func ViewImageSpec() model.ToolSpec {
	return spec(ViewImageToolName, "Show a PNG, JPEG, GIF, or WebP image file to you so you can inspect it visually (screenshots, diagrams, design references). Returns a short description line plus the image.",
		props{"path": "Image file path relative to the working directory"}, "path")
}

// SniffImage returns the MIME type of an accepted image format, decided by
// content rather than file extension, or "" for anything else.
func SniffImage(data []byte) string {
	mime := http.DetectContentType(data)
	if model.ImageMIMETypes[mime] {
		return mime
	}
	return ""
}

// LoadImageFile reads a user-chosen image file (an --image attachment). The
// path is not sandboxed. The type is sniffed from content, and the image's
// Label is the file's base name, so no absolute path reaches the model.
func LoadImageFile(path string, maxBytes int) (model.Image, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxImageBytes
	}
	fi, err := os.Stat(path)
	if err != nil {
		return model.Image{}, err
	}
	if fi.IsDir() {
		return model.Image{}, fmt.Errorf("%s is a directory, not an image", path)
	}
	if fi.Size() > int64(maxBytes) {
		return model.Image{}, fmt.Errorf("%s is %s bytes; max_image_bytes is %s", path, model.FormatBytes(int(fi.Size())), model.FormatBytes(maxBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Image{}, err
	}
	if len(data) > maxBytes {
		return model.Image{}, fmt.Errorf("%s is %s bytes; max_image_bytes is %s", path, model.FormatBytes(len(data)), model.FormatBytes(maxBytes))
	}
	mime := SniffImage(data)
	if mime == "" {
		return model.Image{}, fmt.Errorf("%s is not a PNG, JPEG, GIF, or WebP image", path)
	}
	return model.NewImage(mime, filepath.Base(path), data), nil
}

// ViewImage reads an image under root. The text names the path, MIME type,
// and size, followed by the image's placeholder.
func ViewImage(root, path string, maxBytes int) (string, *model.Image, error) {
	return viewImage(root, path, maxBytes, nil)
}

// viewImage is ViewImage that refuses paths the scope denies for reading.
func viewImage(root, path string, maxBytes int, sc *scope) (string, *model.Image, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxImageBytes
	}
	if path == "" {
		return "", nil, errors.New("path is required")
	}
	full, err := sc.resolve(root, path)
	if err != nil {
		return "", nil, err
	}
	if err := sc.checkRead(full, path); err != nil {
		return "", nil, err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return "", nil, err
	}
	if fi.IsDir() {
		return "", nil, fmt.Errorf("%s is a directory, not an image", path)
	}
	if fi.Size() > int64(maxBytes) {
		return "", nil, fmt.Errorf("%s is %s bytes; max_image_bytes is %s", path, model.FormatBytes(int(fi.Size())), model.FormatBytes(maxBytes))
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", nil, err
	}
	if len(data) > maxBytes {
		return "", nil, fmt.Errorf("%s is %s bytes; max_image_bytes is %s", path, model.FormatBytes(len(data)), model.FormatBytes(maxBytes))
	}
	mime := SniffImage(data)
	if mime == "" {
		return "", nil, fmt.Errorf("%s is not a PNG, JPEG, GIF, or WebP image", path)
	}
	im := model.NewImage(mime, path, data)
	text := fmt.Sprintf("image %s (%s, %s bytes)\n%s", path, mime, model.FormatBytes(len(data)), im.Placeholder())
	return text, &im, nil
}
