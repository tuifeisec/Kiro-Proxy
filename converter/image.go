// image.go
// 图片格式处理：Kiro KiroImage → MIME type / data URI

package converter

import "strings"

// ImageMediaType 将 Kiro format 字段转为完整 MIME type
func ImageMediaType(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return "image/png"
	case "jpeg", "jpg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// ImageDataURI 将 Kiro 图片转为 data URI（OpenAI image_url 格式使用）
func ImageDataURI(img KiroImage) string {
	return "data:" + ImageMediaType(img.Format) + ";base64," + img.Source.Bytes
}
