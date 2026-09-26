package main

// images_test.go - Pixabay image fetch: query shape, hit URL preference,
// fallbacks, and error cases, all against a fake httptest server.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type pxHit struct {
	WebformatURL  string `json:"webformatURL,omitempty"`
	LargeImageURL string `json:"largeImageURL,omitempty"`
	PreviewURL    string `json:"previewURL,omitempty"`
}

func tinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{200, 100, 50, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newPixabayServer serves a tiny PNG at any ".png" path and the hit list at
// "/". Hits are read through a pointer at request time, so callers set them
// AFTER creating the server (its URL only exists then). It asserts the
// request carries the API key and safesearch, and captures q into gotQ.
func newPixabayServer(t *testing.T, hits *[]pxHit, gotQ *string) *httptest.Server {
	t.Helper()
	pngBytes := tinyPNGBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		if got := r.URL.Query().Get("safesearch"); got != "true" {
			t.Errorf("safesearch = %q, want true", got)
		}
		if r.URL.Query().Get("key") == "" {
			t.Error("missing key param")
		}
		if gotQ != nil {
			*gotQ = r.URL.Query().Get("q")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Total int     `json:"total"`
			Hits  []pxHit `json:"hits"`
		}{Total: len(*hits), Hits: *hits})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchPixabayImage(t *testing.T) {
	t.Run("picks webformatURL and downloads it", func(t *testing.T) {
		var hits []pxHit
		srv := newPixabayServer(t, &hits, nil)
		imgURL := srv.URL + "/img.png"
		hits = []pxHit{{PreviewURL: srv.URL + "/preview.png", WebformatURL: imgURL}}
		res := fetchPixabayImage("bali beach", "test-key", srv.URL+"/", nil)
		if res.Err != nil {
			t.Fatalf("fetch: %v", res.Err)
		}
		if res.Image == nil {
			t.Fatal("expected an image")
		}
		if b := res.Image.Bounds(); b.Dx() != 4 || b.Dy() != 3 {
			t.Errorf("decoded %v, want 4x3", b)
		}
		if res.URL != imgURL {
			t.Errorf("URL = %q, want %q (webformatURL)", res.URL, imgURL)
		}
	})

	t.Run("query params", func(t *testing.T) {
		var hits []pxHit
		var gotQ string
		srv := newPixabayServer(t, &hits, &gotQ)
		hits = []pxHit{{WebformatURL: srv.URL + "/img.png"}}
		res := fetchPixabayImage("cute capybara", "k", srv.URL+"/", nil)
		if res.Err != nil {
			t.Fatalf("fetch: %v", res.Err)
		}
		if gotQ != "cute capybara" {
			t.Errorf("q = %q, want %q", gotQ, "cute capybara")
		}
	})

	t.Run("falls back to previewURL when webformat is empty", func(t *testing.T) {
		var hits []pxHit
		srv := newPixabayServer(t, &hits, nil)
		hits = []pxHit{{PreviewURL: srv.URL + "/img2.png"}}
		res := fetchPixabayImage("x", "k", srv.URL+"/", nil)
		if res.Err != nil {
			t.Fatalf("fetch: %v", res.Err)
		}
		if want := srv.URL + "/img2.png"; res.URL != want {
			t.Errorf("URL = %q, want %q", res.URL, want)
		}
	})

	t.Run("skips hits with no usable URL", func(t *testing.T) {
		var hits []pxHit
		srv := newPixabayServer(t, &hits, nil)
		hits = []pxHit{
			{}, // no URLs at all
			{LargeImageURL: srv.URL + "/img.png"},
		}
		res := fetchPixabayImage("x", "k", srv.URL+"/", nil)
		if res.Err != nil {
			t.Fatalf("fetch: %v", res.Err)
		}
		if want := srv.URL + "/img.png"; res.URL != want {
			t.Errorf("URL = %q, want %q (second hit's largeImageURL)", res.URL, want)
		}
	})

	t.Run("no hits is an error naming the keyword", func(t *testing.T) {
		var hits []pxHit
		srv := newPixabayServer(t, &hits, nil)
		res := fetchPixabayImage("xyzzy", "k", srv.URL+"/", nil)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "xyzzy") {
			t.Errorf("Err = %v, want a no-hit error mentioning the keyword", res.Err)
		}
	})

	t.Run("http error surfaces status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid key", http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)
		res := fetchPixabayImage("x", "k", srv.URL+"/", nil)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "400") {
			t.Errorf("Err = %v, want an http 400 error", res.Err)
		}
	})

	t.Run("missing key is a clear error", func(t *testing.T) {
		res := fetchPixabayImage("x", "", "http://unused/", nil)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "no API key") {
			t.Errorf("Err = %v, want a no-key error", res.Err)
		}
	})
}
