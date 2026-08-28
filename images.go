// images.go - conditional image handling for chat replies.
//
// When the model emits an [IMG: <description>] tag, the app either fetches a
// relevant thumbnail from Wikipedia's free REST API (no key needed) or asks a
// Gemini image model to generate one, depending on the -image-source setting.

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ImageResult holds a fetched image plus metadata.
type ImageResult struct {
	Image image.Image
	URL   string
	Err   error
}

const (
	imageTimeout     = 20 * time.Second
	maxImageWidth    = 280 // px inside the bubble
	maxImageHeight   = 180 // px
	imageSearchBase  = "https://en.wikipedia.org/api/rest_v1/page/summary/"
	geminiImageModel = "gemini-3.1-flash-image" // Nano Banana 2
)

// GenerateImage asks a Gemini image model to create a picture from the prompt.
// It returns the decoded image scaled to the bubble size, or an error if the
// model returns no image part.
func (b *Bot) GenerateImage(prompt string) (image.Image, error) {
	g := &geminiProvider{apiKey: b.APIKey, apiURL: b.APIURL, model: geminiImageModel, http: b.HTTP}
	return g.generateImage(prompt)
}

// FetchImage searches Wikipedia for the keyword and downloads the thumbnail.
func FetchImage(keyword string, client *http.Client) *ImageResult {
	if client == nil {
		client = &http.Client{Timeout: imageTimeout}
	}
	encoded := url.PathEscape(strings.ReplaceAll(keyword, " ", "_"))
	summaryURL := imageSearchBase + encoded

	req, err := http.NewRequest(http.MethodGet, summaryURL, nil)
	if err != nil {
		return &ImageResult{Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chat-app/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return &ImageResult{Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &ImageResult{Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return &ImageResult{Err: fmt.Errorf("wikipedia summary http %d: %s", resp.StatusCode, summaryURL)}
	}

	var summary struct {
		Thumbnail *struct {
			Source string `json:"source"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"thumbnail"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return &ImageResult{Err: err}
	}
	if summary.Thumbnail == nil || summary.Thumbnail.Source == "" {
		return &ImageResult{Err: fmt.Errorf("no thumbnail for %q", keyword)}
	}

	img, err := downloadImage(summary.Thumbnail.Source, client)
	if err != nil {
		return &ImageResult{Err: err}
	}
	return &ImageResult{Image: img, URL: summary.Thumbnail.Source}
}

func downloadImage(imgURL string, client *http.Client) (image.Image, error) {
	req, err := http.NewRequest(http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chat-app/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download http %d", resp.StatusCode)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "png"):
		return png.Decode(io.LimitReader(resp.Body, 1<<22))
	default:
		return jpeg.Decode(io.LimitReader(resp.Body, 1<<22))
	}
}

// scaleImage scales an image down to fit within maxImageWidth x maxImageHeight
// while preserving aspect ratio. Images already smaller are returned as-is.
func scaleImage(src image.Image) image.Image {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= maxImageWidth && h <= maxImageHeight {
		return src
	}
	scaleW := float64(maxImageWidth) / float64(w)
	scaleH := float64(maxImageHeight) / float64(h)
	scale := scaleW
	if scaleH < scaleW {
		scale = scaleH
	}
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	// nearest-neighbor downscale is fine for thumbnails
	for y := 0; y < newH; y++ {
		sy := bounds.Min.Y + (y*h)/newH
		for x := 0; x < newW; x++ {
			sx := bounds.Min.X + (x*w)/newW
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// CleanKeyword strips anything after a newline and trims punctuation.
var sanitizeKeyword = regexp.MustCompile(`[\n\r]`)

func CleanKeyword(k string) string {
	k = sanitizeKeyword.ReplaceAllString(k, " ")
	k = strings.TrimSpace(k)
	k = strings.Trim(k, `.,;:!?"'()[]{}`)
	return k
}
