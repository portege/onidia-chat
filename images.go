// images.go - conditional image handling for chat replies.
//
// When the model emits an [IMG: <description>] tag, the app either fetches a
// relevant photo from Pixabay (default), a thumbnail from Wikipedia's free
// REST API (no key needed), or asks a Gemini image model to generate one,
// depending on the -image-source setting.

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
	pixabayAPIBase   = "https://pixabay.com/api/"
	geminiImageModel = "gemini-3.1-flash-image" // Nano Banana 2

	// defaultPixabayKey is a free shared key baked in so image replies work
	// out of the box; override with -pixabay-key, $PIXABAY_API_KEY, or
	// pixabay-key in chat-app.ini. Rotate it if it ever leaks.
	defaultPixabayKey = "57324196-80f538aa87774c57d2e3c7b71"
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

// FetchPixabayImage searches Pixabay for the keyword and downloads the best
// hit's web-format photo (640px, a good fit for the chat bubble).
func FetchPixabayImage(keyword, apiKey string, client *http.Client) *ImageResult {
	return fetchPixabayImage(keyword, apiKey, pixabayAPIBase, client)
}

// fetchPixabayImage is the testable core: baseURL lets tests point at a fake
// server instead of the real Pixabay API.
func fetchPixabayImage(keyword, apiKey, baseURL string, client *http.Client) *ImageResult {
	if strings.TrimSpace(apiKey) == "" {
		return &ImageResult{Err: fmt.Errorf("pixabay: no API key (set pixabay-key in chat-app.ini, $PIXABAY_API_KEY, or -pixabay-key)")}
	}
	if client == nil {
		client = &http.Client{Timeout: imageTimeout}
	}

	searchURL := baseURL + "?key=" + url.QueryEscape(strings.TrimSpace(apiKey)) +
		"&safesearch=true&per_page=3&q=" + url.QueryEscape(keyword)
	req, err := http.NewRequest(http.MethodGet, searchURL, nil)
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
		body := string(raw)
		if len(body) > 200 {
			body = body[:200] + "..."
		}
		return &ImageResult{Err: fmt.Errorf("pixabay http %d: %s", resp.StatusCode, body)}
	}

	var result struct {
		Hits []struct {
			WebformatURL  string `json:"webformatURL"`
			LargeImageURL string `json:"largeImageURL"`
			PreviewURL    string `json:"previewURL"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return &ImageResult{Err: err}
	}

	imgURL := ""
	for _, hit := range result.Hits {
		// 640px web format is the sweet spot for a 260px bubble; fall back to
		// the larger or smaller variant when a hit lacks it.
		for _, cand := range []string{hit.WebformatURL, hit.LargeImageURL, hit.PreviewURL} {
			if cand != "" {
				imgURL = cand
				break
			}
		}
		if imgURL != "" {
			break
		}
	}
	if imgURL == "" {
		return &ImageResult{Err: fmt.Errorf("no pixabay hit for %q", keyword)}
	}

	img, err := downloadImage(imgURL, client)
	if err != nil {
		return &ImageResult{Err: err}
	}
	return &ImageResult{Image: img, URL: imgURL}
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
