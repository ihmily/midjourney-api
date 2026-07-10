package oss

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/pkg/constants"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func useImageDownloadRoundTripper(t *testing.T, transport http.RoundTripper) {
	t.Helper()

	originalClient := imageDownloadClient
	imageDownloadClient = &http.Client{
		Timeout:   constants.DefaultHTTPTimeout,
		Transport: transport,
	}
	t.Cleanup(func() {
		imageDownloadClient = originalClient
	})
}

func useImageDownloadResponse(t *testing.T, resp *http.Response) {
	t.Helper()

	useImageDownloadRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if resp.Header == nil {
			resp.Header = make(http.Header)
		}
		if resp.Body == nil {
			resp.Body = io.NopCloser(strings.NewReader(""))
		}
		resp.Request = req
		return resp, nil
	}))
}

func TestUploaderConstructorsRejectNilConfig(t *testing.T) {
	tests := []struct {
		name string
		run  func() (Uploader, error)
		want string
	}{
		{
			name: "top level",
			run: func() (Uploader, error) {
				return NewUploader(nil, nil)
			},
			want: "oss config",
		},
		{
			name: "s3",
			run: func() (Uploader, error) {
				return newS3Uploader(nil, nil)
			},
			want: "oss.s3 config",
		},
		{
			name: "aliyun",
			run: func() (Uploader, error) {
				return newAliyunUploader(nil, nil)
			},
			want: "oss.aliyun config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uploader, err := tt.run()

			if uploader != nil {
				t.Fatalf("uploader = %#v, want nil", uploader)
			}
			if err == nil {
				t.Fatal("constructor returned nil error, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestNewUploaderReturnsNilWhenDisabled(t *testing.T) {
	uploader, err := NewUploader(&appconfig.OSSConfig{Enable: false}, nil)

	if err != nil {
		t.Fatalf("NewUploader returned error: %v", err)
	}
	if uploader != nil {
		t.Fatalf("uploader = %#v, want nil when disabled", uploader)
	}
}

func TestDownloadImageRedactsTransportError(t *testing.T) {
	originalClient := imageDownloadClient
	defer func() {
		imageDownloadClient = originalClient
	}()

	imageDownloadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New(`transport failed for https://user:pass@cdn.example.com/image.png?token=secret#fragment custom_id="secret-custom-id"`)
		}),
	}

	imageURL := "https://cdn.example.com/image.png?token=secret#fragment"
	_, _, err := downloadImage(context.Background(), imageURL)
	if err == nil {
		t.Fatalf("expected transport error")
	}

	msg := err.Error()
	for _, forbidden := range []string{
		"token=secret",
		"user:pass",
		"#fragment",
		"custom_id",
		"secret-custom-id",
	} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("download error exposed %q: %s", forbidden, msg)
		}
	}
	if !strings.Contains(msg, "https://cdn.example.com/image.png") || !strings.Contains(msg, "<redacted>") {
		t.Fatalf("download error did not keep useful redacted context: %s", msg)
	}
}

func TestOSSSDKErrorRedactsAndPreservesCause(t *testing.T) {
	cause := errors.New(`request failed access_key_id=storage-key secret_access_key=storage-secret url=https://user:pass@oss.example.com/object.png?OSSAccessKeyId=key&Signature=sig#frag`)

	err := ossSDKError("S3 PutObject failed", cause)

	if err == nil {
		t.Fatal("ossSDKError returned nil")
	}
	if !errors.Is(err, cause) {
		t.Fatal("ossSDKError did not preserve the original cause")
	}
	msg := err.Error()
	if !strings.Contains(msg, "S3 PutObject failed") {
		t.Fatalf("error = %q, want operation context", msg)
	}
	for _, forbidden := range []string{"storage-key", "storage-secret", "OSSAccessKeyId=key", "Signature=sig", "user:pass", "#frag"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("ossSDKError exposed %q: %s", forbidden, msg)
		}
	}
	if !strings.Contains(msg, "access_key_id=<redacted>") ||
		!strings.Contains(msg, "secret_access_key=<redacted>") ||
		!strings.Contains(msg, "https://oss.example.com/object.png") {
		t.Fatalf("ossSDKError did not keep useful redacted context: %s", msg)
	}
}

func TestOSSSDKErrorAllowsNil(t *testing.T) {
	if err := ossSDKError("S3 PutObject failed", nil); err != nil {
		t.Fatalf("ossSDKError nil = %v, want nil", err)
	}
}

func TestDownloadImageRejectsOversizedContentLength(t *testing.T) {
	useImageDownloadResponse(t, &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: constants.MaxImageDownloadBytes + 1,
	})

	_, _, err := downloadImage(context.Background(), "https://cdn.example.com/image.png")
	if err == nil {
		t.Fatalf("expected oversized image error")
	}
	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("error = %v, want max size error", err)
	}
}

func TestDownloadImageErrorRedactsURLQuery(t *testing.T) {
	useImageDownloadResponse(t, &http.Response{
		StatusCode: http.StatusForbidden,
	})

	imageURL := "https://cdn.example.com/image.png?token=secret-token#fragment"
	_, _, err := downloadImage(context.Background(), imageURL)
	if err == nil {
		t.Fatalf("expected download error")
	}

	msg := err.Error()
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "token=") || strings.Contains(msg, "fragment") {
		t.Fatalf("error exposed sensitive URL parts: %s", msg)
	}
	if !strings.Contains(msg, "/image.png") {
		t.Fatalf("error did not keep useful URL path context: %s", msg)
	}
}

func TestDownloadImageRejectsNonImageContentType(t *testing.T) {
	useImageDownloadResponse(t, &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/html; charset=utf-8"},
		},
		Body: io.NopCloser(strings.NewReader("<html>not an image</html>")),
	})

	imageURL := "https://cdn.example.com/image.png?token=secret-token"
	_, _, err := downloadImage(context.Background(), imageURL)
	if err == nil {
		t.Fatalf("expected non-image content type error")
	}

	msg := err.Error()
	if !strings.Contains(msg, "non-image content type") {
		t.Fatalf("error = %q, want non-image content type context", msg)
	}
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "token=") {
		t.Fatalf("error exposed sensitive URL query: %s", msg)
	}
}

func TestDownloadImageRejectsSVGContentType(t *testing.T) {
	useImageDownloadResponse(t, &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"image/svg+xml"},
		},
		Body: io.NopCloser(strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)),
	})

	_, _, err := downloadImage(context.Background(), "https://cdn.example.com/image.svg")
	if err == nil {
		t.Fatalf("expected svg content type error")
	}
	if !strings.Contains(err.Error(), "non-image content type") {
		t.Fatalf("error = %q, want non-image content type context", err)
	}
}

func TestDownloadImageRejectsDisguisedTextBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "html",
			body: "<!doctype html><html>not an image</html>",
		},
		{
			name: "svg",
			body: `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useImageDownloadResponse(t, &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"image/png"},
				},
				Body: io.NopCloser(strings.NewReader(tt.body)),
			})

			_, _, err := downloadImage(context.Background(), "https://cdn.example.com/image.png?token=secret")
			if err == nil {
				t.Fatalf("expected disguised body error")
			}
			if !strings.Contains(err.Error(), "body content type") {
				t.Fatalf("error = %q, want body content type context", err)
			}
			if strings.Contains(err.Error(), "token=secret") || strings.Contains(err.Error(), "token") {
				t.Fatalf("error exposed sensitive URL query: %s", err.Error())
			}
		})
	}
}

func TestDownloadImageRejectsNonHTTPURL(t *testing.T) {
	_, _, err := downloadImage(context.Background(), "ftp://example.com/image.png")
	if err == nil {
		t.Fatalf("expected non-http URL error")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("error = %q, want scheme context", err)
	}
}

func TestDownloadImageRejectsUserinfoURL(t *testing.T) {
	_, _, err := downloadImage(context.Background(), "https://user:pass@cdn.example.com/image.png")
	if err == nil {
		t.Fatalf("expected userinfo URL error")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("error = %q, want userinfo context", err.Error())
	}
	if strings.Contains(err.Error(), "user:pass") || strings.Contains(err.Error(), "pass@") {
		t.Fatalf("error exposed URL userinfo: %s", err.Error())
	}
}

func TestDownloadImageRejectsPrivateIPLiteralBeforeHTTP(t *testing.T) {
	called := false
	useImageDownloadRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("transport should not be called")
	}))

	_, _, err := downloadImage(context.Background(), "http://127.0.0.1/image.png?token=secret#fragment")
	if err == nil {
		t.Fatalf("expected private/local URL error")
	}
	if called {
		t.Fatal("image download transport was called for a private IP literal")
	}
	if !strings.Contains(err.Error(), "private or local address") {
		t.Fatalf("error = %q, want private/local context", err.Error())
	}
	for _, forbidden := range []string{"token=secret", "#fragment"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed %q: %s", forbidden, err.Error())
		}
	}
}

func TestImageDownloadClientRejectsPrivateNetworkAddress(t *testing.T) {
	originalClient := imageDownloadClient
	imageDownloadClient = newImageDownloadHTTPClient()
	t.Cleanup(func() {
		imageDownloadClient = originalClient
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("private-network request unexpectedly reached test server")
	}))
	defer server.Close()

	localhostURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	_, _, err := downloadImage(context.Background(), localhostURL+"/image.png?token=secret")
	if err == nil {
		t.Fatalf("expected private network rejection")
	}
	if !strings.Contains(err.Error(), "private or local address") {
		t.Fatalf("error = %q, want private/local context", err.Error())
	}
	if strings.Contains(err.Error(), "token=secret") || strings.Contains(err.Error(), "token") {
		t.Fatalf("error exposed sensitive URL query: %s", err.Error())
	}
}

func TestDownloadImageAcceptsImageContentTypeWithParameters(t *testing.T) {
	useImageDownloadResponse(t, &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"image/png; charset=binary"},
		},
		Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
	})

	data, contentType, err := downloadImage(context.Background(), "https://cdn.example.com/image.png")
	if err != nil {
		t.Fatalf("downloadImage returned error: %v", err)
	}
	if string(data) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("data = %q, want png signature", string(data))
	}
	if contentType != "image/png; charset=binary" {
		t.Fatalf("contentType = %q, want original content type", contentType)
	}
}

func TestDownloadImageUsesNormalizedRequestURL(t *testing.T) {
	var requestedURL string
	useImageDownloadRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"image/png"},
			},
			Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
		}, nil
	}))

	_, _, err := downloadImage(context.Background(), "  https://cdn.example.com/a file.png?token=secret#fragment  ")
	if err != nil {
		t.Fatalf("downloadImage returned error: %v", err)
	}

	if requestedURL != "https://cdn.example.com/a%20file.png?token=secret#fragment" {
		t.Fatalf("request URL = %q, want normalized URL", requestedURL)
	}
}

func TestDownloadImageDetectsImageWhenContentTypeMissing(t *testing.T) {
	originalClient := imageDownloadClient
	defer func() {
		imageDownloadClient = originalClient
	}()

	imageDownloadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
			}, nil
		}),
	}

	data, contentType, err := downloadImage(context.Background(), "https://cdn.example.com/image")
	if err != nil {
		t.Fatalf("downloadImage returned error: %v", err)
	}
	if string(data) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("data = %q, want png signature", string(data))
	}
	if contentType != "image/png" {
		t.Fatalf("contentType = %q, want detected image/png", contentType)
	}
}

func TestDownloadImageRejectsMissingContentTypeWhenBodyIsNotImage(t *testing.T) {
	originalClient := imageDownloadClient
	defer func() {
		imageDownloadClient = originalClient
	}()

	imageDownloadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("<html>not an image</html>")),
			}, nil
		}),
	}

	_, _, err := downloadImage(context.Background(), "https://cdn.example.com/not-image?token=secret")
	if err == nil {
		t.Fatalf("expected non-image error")
	}
	if !strings.Contains(err.Error(), "non-image content type") {
		t.Fatalf("error = %v, want non-image content type", err)
	}
	if strings.Contains(err.Error(), "token=secret") {
		t.Fatalf("error exposed sensitive URL query: %s", err.Error())
	}
}

func TestLogURLRedactsQueryAndFragment(t *testing.T) {
	got := logURL("https://bucket.oss.example.com/path/image.png?OSSAccessKeyId=secret&Signature=sig#fragment")

	if strings.Contains(got, "secret") || strings.Contains(got, "Signature") || strings.Contains(got, "fragment") || strings.Contains(got, "?") {
		t.Fatalf("log URL exposed sensitive parts: %s", got)
	}
	if got != "https://bucket.oss.example.com/path/image.png" {
		t.Fatalf("logURL = %q, want URL path preserved without query", got)
	}
}

func TestBuildObjectKeySanitizesFilenameAndPrefix(t *testing.T) {
	key := buildObjectKey(" /mid journey/../results?/ ", "../task 1", "https://cdn.example.com/a%2Fb%20(1).png?token=secret")

	if key != "mid_journey/results/task_1-a_b_1_.png" {
		t.Fatalf("key = %q, want sanitized object key", key)
	}
	if strings.ContainsAny(key, " ?#") || strings.Contains(key, "%2F") {
		t.Fatalf("key contains unsafe URL-derived characters: %q", key)
	}
}

func TestObjectKeyURLPathEscapesSegmentsAndPreservesSlashes(t *testing.T) {
	got := objectKeyURLPath("mid journey/结果/task 1-hello?#.png")
	want := "mid%20journey/%E7%BB%93%E6%9E%9C/task%201-hello%3F%23.png"

	if got != want {
		t.Fatalf("objectKeyURLPath = %q, want %q", got, want)
	}
}

func TestS3PublicURLEscapesObjectKey(t *testing.T) {
	uploader := &s3Uploader{
		cfg: &appconfig.S3Config{
			EndpointURL: "https://s3.example.com",
			BucketName:  "bucket",
			Region:      "us-east-1",
		},
	}

	got := uploader.buildPublicURL("mid journey/结果/task 1-hello?#.png")
	want := "https://s3.example.com/bucket/mid%20journey/%E7%BB%93%E6%9E%9C/task%201-hello%3F%23.png"
	if got != want {
		t.Fatalf("buildPublicURL = %q, want %q", got, want)
	}
}

func TestS3AWSPublicURLEscapesObjectKey(t *testing.T) {
	uploader := &s3Uploader{
		cfg: &appconfig.S3Config{
			BucketName: "bucket",
			Region:     "us-east-1",
		},
	}

	got := uploader.buildPublicURL("mid journey/task 1?.png")
	want := "https://bucket.s3.us-east-1.amazonaws.com/mid%20journey/task%201%3F.png"
	if got != want {
		t.Fatalf("buildPublicURL = %q, want %q", got, want)
	}
}

func TestAliyunPublicURLEscapesObjectKey(t *testing.T) {
	uploader := &aliyunUploader{
		cfg: &appconfig.AliyunOSSConfig{
			Endpoint:   "https://oss-cn.example.aliyuncs.com",
			BucketName: "bucket",
		},
	}

	got, err := uploader.buildURL("mid journey/结果/task 1-hello?#.png")
	if err != nil {
		t.Fatalf("buildURL returned error: %v", err)
	}
	want := "https://bucket.oss-cn.example.aliyuncs.com/mid%20journey/%E7%BB%93%E6%9E%9C/task%201-hello%3F%23.png"
	if got != want {
		t.Fatalf("buildURL = %q, want %q", got, want)
	}
}

func TestAliyunPublicURLHandlesUppercaseEndpointScheme(t *testing.T) {
	uploader := &aliyunUploader{
		cfg: &appconfig.AliyunOSSConfig{
			Endpoint:   "HTTPS://oss-cn.example.aliyuncs.com",
			BucketName: "bucket",
		},
	}

	got, err := uploader.buildURL("task-1-image.png")
	if err != nil {
		t.Fatalf("buildURL returned error: %v", err)
	}
	want := "https://bucket.oss-cn.example.aliyuncs.com/task-1-image.png"
	if got != want {
		t.Fatalf("buildURL = %q, want %q", got, want)
	}
}

func TestAliyunCnamePublicURLEscapesObjectKey(t *testing.T) {
	uploader := &aliyunUploader{
		cfg: &appconfig.AliyunOSSConfig{
			IsCname:     true,
			CnameDomain: "https://cdn.example.com",
		},
	}

	got, err := uploader.buildURL("mid journey/task 1?.png")
	if err != nil {
		t.Fatalf("buildURL returned error: %v", err)
	}
	want := "https://cdn.example.com/mid%20journey/task%201%3F.png"
	if got != want {
		t.Fatalf("buildURL = %q, want %q", got, want)
	}
}

func TestBuildObjectKeyFallsBackWhenFilenameIsNotUsable(t *testing.T) {
	key := buildObjectKey("", "task-2", "https://cdn.example.com/%E4%BD%A0%E5%A5%BD")

	if key != "task-2-image.png" {
		t.Fatalf("key = %q, want task fallback filename", key)
	}
}

func TestBuildObjectKeyFallsBackWhenTaskIDIsNotUsable(t *testing.T) {
	key := buildObjectKey("", "../", "https://cdn.example.com/image.png")

	if key != "task-image.png" {
		t.Fatalf("key = %q, want safe task fallback", key)
	}
}

func TestSanitizeObjectFilenameTruncatesBeforeExtension(t *testing.T) {
	filename := sanitizeObjectFilename(strings.Repeat("a", maxObjectFilenameLength+20) + ".png")

	if len(filename) != maxObjectFilenameLength {
		t.Fatalf("filename len = %d, want %d", len(filename), maxObjectFilenameLength)
	}
	if !strings.HasSuffix(filename, ".png") {
		t.Fatalf("filename = %q, want extension preserved", filename)
	}
}
