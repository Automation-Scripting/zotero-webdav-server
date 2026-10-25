package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const bridgeBodyLimit = 96 * 1024

type workerRequest struct {
	Path          string `json:"path"`
	Method        string `json:"method,omitempty"`
	ContentType   string `json:"contentType,omitempty"`
	ContentLength *int64 `json:"contentLength,omitempty"`
	Depth         string `json:"depth,omitempty"`
	Body          string `json:"body,omitempty"`
}

func (app *application) handleWorkerBridge(ctx context.Context, request events.APIGatewayV2HTTPRequest, endpoint string) (events.APIGatewayV2HTTPResponse, error) {
	// Basic credentials have already been validated by handle(). Require a second
	// credential for this internal protocol; disabled unless explicitly configured.
	if len(app.workerToken) < 32 {
		return textResponse(404, "Not found.\n"), nil
	}
	got := sha256.Sum256([]byte(getHeader(request.Headers, "x-webdav-worker-token")))
	want := sha256.Sum256([]byte(app.workerToken))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return textResponse(403, "Forbidden.\n"), nil
	}
	if request.RequestContext.HTTP.Method != "POST" {
		return textResponse(405, "POST required.\n"), nil
	}
	if len(request.Body) > bridgeBodyLimit*2 {
		return textResponse(413, "Metadata too large.\n"), nil
	}
	body, err := requestBody(request)
	if err != nil || len(body) > bridgeBodyLimit {
		return textResponse(400, "Invalid metadata.\n"), nil
	}
	var input workerRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return textResponse(400, "Invalid metadata.\n"), nil
	}
	// Reject alternate URL spellings instead of letting path.Clean retarget a write.
	decoded, err := url.PathUnescape(input.Path)
	if err != nil || !strings.HasPrefix(input.Path, "/") || strings.ContainsAny(decoded, "\\\x00?#") || strings.TrimSpace(decoded) != decoded {
		return textResponse(400, "Invalid path.\n"), nil
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return textResponse(400, "Invalid path.\n"), nil
		}
	}
	key, collection, err := app.resolvePath(input.Path)
	if err != nil {
		return textResponse(400, "Invalid path.\n"), nil
	}
	if endpoint == "/_worker/dav" {
		forwarded := request
		forwarded.RawPath = input.Path
		forwarded.Body = input.Body
		forwarded.IsBase64Encoded = false
		forwarded.Headers = map[string]string{"depth": input.Depth}
		switch input.Method {
		case "PROPFIND":
			return app.handlePropfind(ctx, forwarded, key, collection)
		case "MKCOL":
			if input.Body != "" {
				return textResponse(415, "MKCOL body unsupported.\n"), nil
			}
			return app.handleMkcol(ctx, key, collection)
		default:
			return textResponse(405, "Unsupported bridge method.\n"), nil
		}
	}
	if collection || key == app.rootKey() {
		return textResponse(409, "Object path required.\n"), nil
	}
	if input.Method != "" || input.Body != "" || input.Depth != "" {
		return textResponse(400, "Upload metadata only.\n"), nil
	}
	if input.ContentLength == nil || *input.ContentLength < 0 || *input.ContentLength > 5_000_000_000 {
		return textResponse(400, "Invalid upload length.\n"), nil
	}
	if input.ContentType == "" || len(input.ContentType) > 255 || strings.ContainsAny(input.ContentType, "\r\n") {
		return textResponse(400, "Invalid content type.\n"), nil
	}
	lifetime := app.putPresignLifetime
	if lifetime <= 0 || lifetime > 300*time.Second {
		lifetime = 120 * time.Second
	}
	// With no payload available, do not sign the SDK's checksum of an empty body.
	signer := s3.NewPresignClient(app.s3, s3.WithPresignClientFromClientOptions(func(o *s3.Options) {
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}))
	signed, err := signer.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(app.bucket), Key: aws.String(key),
		ContentType: aws.String(input.ContentType), ContentLength: input.ContentLength,
	}, func(o *s3.PresignOptions) { o.Expires = lifetime })
	if err != nil {
		return serverError(), nil
	} // Do not log SDK errors containing signed requests.
	payload, err := json.Marshal(struct {
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		ExpiresIn int64             `json:"expiresIn"`
	}{signed.URL, map[string]string{"Content-Type": input.ContentType, "Content-Length": strconv.FormatInt(*input.ContentLength, 10)}, int64(lifetime / time.Second)})
	if err != nil {
		return serverError(), nil
	}
	return response(200, map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"}, string(payload)), nil
}
