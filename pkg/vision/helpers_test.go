package vision

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testVisionProvider struct {
	analyzeImageFunc func(ctx context.Context, imageData []byte, mimeType string) (*AnalysisResult, error)
	analyzeImageHit  bool
}

func (p *testVisionProvider) Name() string {
	return "test"
}

func (p *testVisionProvider) AnalyzeImage(ctx context.Context, imageData []byte, mimeType string) (*AnalysisResult, error) {
	p.analyzeImageHit = true
	if p.analyzeImageFunc != nil {
		return p.analyzeImageFunc(ctx, imageData, mimeType)
	}
	return &AnalysisResult{}, nil
}

func (p *testVisionProvider) AnalyzeImageURL(ctx context.Context, imageURL string) (*AnalysisResult, error) {
	return nil, errors.New("not implemented")
}

func (p *testVisionProvider) OCR(ctx context.Context, imageData []byte, mimeType string) ([]DetectedText, error) {
	return nil, errors.New("not implemented")
}

func (p *testVisionProvider) LabelImage(ctx context.Context, imageData []byte, mimeType string) ([]Label, error) {
	return nil, errors.New("not implemented")
}

func (p *testVisionProvider) DetectObjects(ctx context.Context, imageData []byte, mimeType string) ([]DetectedObject, error) {
	return nil, errors.New("not implemented")
}

func TestImageDataURLRoundTrip(t *testing.T) {
	data := []byte("image-bytes")
	mimeType := "image/png"

	dataURL := ImageToDataURL(data, mimeType)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("unexpected data URL prefix: %s", dataURL)
	}

	decoded, gotMimeType, err := ImageFromDataURL(dataURL)
	if err != nil {
		t.Fatalf("ImageFromDataURL: %v", err)
	}
	if gotMimeType != mimeType {
		t.Fatalf("expected mime type %s, got %s", mimeType, gotMimeType)
	}
	if string(decoded) != string(data) {
		t.Fatalf("expected decoded data %q, got %q", string(data), string(decoded))
	}
}

func TestImageFromDataURLRejectsInvalidInput(t *testing.T) {
	if _, _, err := ImageFromDataURL("not-a-data-url"); err == nil {
		t.Fatal("expected non-data URL to fail")
	}
	if _, _, err := ImageFromDataURL("data:image/png;base64"); err == nil {
		t.Fatal("expected malformed data URL to fail")
	}
}

func TestDetectMediaType(t *testing.T) {
	cases := []struct {
		mimeType string
		want     string
	}{
		{mimeType: "image/jpeg", want: "image"},
		{mimeType: "video/mp4", want: "video"},
		{mimeType: "audio/mpeg", want: "audio"},
		{mimeType: "application/json", want: ""},
	}

	for _, tc := range cases {
		if got := detectMediaType(tc.mimeType); got != tc.want {
			t.Fatalf("detectMediaType(%q) = %q, want %q", tc.mimeType, got, tc.want)
		}
	}
}

func TestAnalyzeMediaRejectsUnsupportedType(t *testing.T) {
	pipeline := NewMediaUnderstandingPipeline(DefaultMediaUnderstandingConfig())
	if _, err := AnalyzeMedia(context.Background(), pipeline, []byte("x"), "application/json"); err == nil {
		t.Fatal("expected unsupported media type error")
	}
}

func TestUnderstandImageRejectsOversizedEncodedImage(t *testing.T) {
	provider := &testVisionProvider{}
	pipeline := NewMediaUnderstandingPipeline(MediaUnderstandingConfig{
		VisionProvider: provider,
		MaxImageSize:   4,
		Timeout:        DefaultMediaUnderstandingConfig().Timeout,
	})

	_, err := pipeline.UnderstandImage(context.Background(), []byte("12345"), "image/png")
	if err == nil {
		t.Fatal("expected oversized image to fail")
	}
	if !strings.Contains(err.Error(), "image too large") {
		t.Fatalf("expected image size error, got %v", err)
	}
	if provider.analyzeImageHit {
		t.Fatal("expected provider not to be called for oversized image")
	}
}

func TestUnderstandImageCallsProviderForValidSizedImage(t *testing.T) {
	provider := &testVisionProvider{
		analyzeImageFunc: func(ctx context.Context, imageData []byte, mimeType string) (*AnalysisResult, error) {
			return &AnalysisResult{
				Description: "a cat",
				Labels:      []Label{{Name: "cat"}, {Name: "pet"}},
			}, nil
		},
	}
	pipeline := NewMediaUnderstandingPipeline(MediaUnderstandingConfig{
		VisionProvider: provider,
		MaxImageSize:   16,
		Timeout:        DefaultMediaUnderstandingConfig().Timeout,
	})

	result, err := pipeline.UnderstandImage(context.Background(), []byte("12345"), "image/png")
	if err != nil {
		t.Fatalf("UnderstandImage: %v", err)
	}
	if !provider.analyzeImageHit {
		t.Fatal("expected provider to be called")
	}
	if result.Description != "a cat" {
		t.Fatalf("expected description %q, got %q", "a cat", result.Description)
	}
	if result.Summary != "cat, pet" {
		t.Fatalf("expected summary %q, got %q", "cat, pet", result.Summary)
	}
}

func TestMimeTypeFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{path: "photo.jpg", want: "image/jpeg"},
		{path: "photo.JPEG", want: "image/jpeg"},
		{path: "photo.png", want: "image/png"},
		{path: "photo.gif", want: "image/gif"},
		{path: "photo.webp", want: "image/webp"},
		{path: "photo.bmp", want: "image/bmp"},
		{path: "photo.txt", want: ""},
	}

	for _, tc := range cases {
		if got := mimeTypeFromPath(tc.path); got != tc.want {
			t.Fatalf("mimeTypeFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestLikelihoodToFloat(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{input: "VERY_UNLIKELY", want: 0.1},
		{input: "UNLIKELY", want: 0.3},
		{input: "POSSIBLE", want: 0.5},
		{input: "LIKELY", want: 0.7},
		{input: "VERY_LIKELY", want: 0.9},
		{input: "UNKNOWN", want: 0},
	}

	for _, tc := range cases {
		if got := likelihoodToFloat(tc.input); got != tc.want {
			t.Fatalf("likelihoodToFloat(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestParseFPS(t *testing.T) {
	if got := parseFPS("30000/1001"); got < 29.9 || got > 30.0 {
		t.Fatalf("expected NTSC-ish fps, got %v", got)
	}
	if got := parseFPS("24"); got != 24 {
		t.Fatalf("expected 24 fps, got %v", got)
	}
	if got := parseFPS("invalid"); got != 0 {
		t.Fatalf("expected invalid input to parse as 0, got %v", got)
	}
}

func TestAudioAnalyzerHelpers(t *testing.T) {
	analyzer := NewAudioAnalyzer()

	if got := analyzer.calcSilenceRatio([]SilenceSegment{{Duration: 2}, {Duration: 3}}, 10); got != 0.5 {
		t.Fatalf("expected silence ratio 0.5, got %v", got)
	}
	if got := analyzer.calcSilenceRatio(nil, 0); got != 0 {
		t.Fatalf("expected zero silence ratio for zero duration, got %v", got)
	}

	if got := analyzer.calcEnergyVariance([]float64{1, 2, 3}); got <= 0 {
		t.Fatalf("expected positive variance, got %v", got)
	}
	if got := analyzer.calcEnergyVariance([]float64{1}); got != 0 {
		t.Fatalf("expected zero variance for one sample, got %v", got)
	}
}

func TestJSONUnmarshal(t *testing.T) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := jsonUnmarshal([]byte(`{"name":"vision"}`), &payload); err != nil {
		t.Fatalf("jsonUnmarshal: %v", err)
	}
	if payload.Name != "vision" {
		t.Fatalf("expected parsed name, got %q", payload.Name)
	}

	if err := jsonUnmarshal([]byte(`{invalid}`), &payload); err == nil {
		t.Fatal("expected invalid JSON to fail")
	}

	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("expected payload to remain JSON-marshalable, err=%v", err)
	}
}
