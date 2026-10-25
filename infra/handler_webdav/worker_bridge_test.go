package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func bridgeFixture() (*application, events.APIGatewayV2HTTPRequest) {
	client := s3.NewFromConfig(aws.Config{Region: "us-east-2", Credentials: credentials.NewStaticCredentialsProvider("TESTKEY", "TESTSECRET", "")})
	app := &application{bucket: "test-webdav-poc", prefix: "zotero", username: "lab", password: "synthetic", workerToken: strings.Repeat("x", 32), putPresignLifetime: 120 * time.Second, s3: client}
	req := events.APIGatewayV2HTTPRequest{RawPath: "/_worker/upload", Headers: map[string]string{
		"authorization":         "Basic " + base64.StdEncoding.EncodeToString([]byte("lab:synthetic")),
		"x-webdav-worker-token": app.workerToken,
	}, Body: `{"path":"/zotero/ABCD1234.zip","contentLength":33554432,"contentType":"application/zip"}`}
	req.RequestContext.HTTP.Method = "POST"
	return app, req
}
func TestBridgePresignsLargeUploadWithoutPayload(t *testing.T) {
	app, req := bridgeFixture()
	result, err := app.handle(context.Background(), req)
	if err != nil || result.StatusCode != 200 {
		t.Fatalf("status=%d err=%v", result.StatusCode, err)
	}
	var output struct {
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		ExpiresIn int               `json:"expiresIn"`
	}
	if err = json.Unmarshal([]byte(result.Body), &output); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(output.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "test-webdav-poc.s3.us-east-2.amazonaws.com" || u.Path != "/zotero/ABCD1234.zip" {
		t.Fatal("incorrect destination")
	}
	if output.ExpiresIn != 120 || u.Query().Get("X-Amz-Expires") != "120" {
		t.Fatal("wrong expiration")
	}
	if output.Headers["Content-Length"] != "33554432" || output.Headers["Content-Type"] != "application/zip" {
		t.Fatal("wrong headers")
	}
	if strings.Contains(strings.ToLower(output.URL), "checksum") {
		t.Fatal("signed empty payload checksum")
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("missing signature")
	}
	for _, h := range strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";") {
		if h != "host" && h != "content-length" && h != "content-type" {
			t.Fatalf("unexpected required header: %s", h)
		}
	}
}
func TestBridgeRejectsUnauthorizedAndMalformedRequests(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*application, *events.APIGatewayV2HTTPRequest)
		status int
	}{
		{"wrong basic", func(a *application, r *events.APIGatewayV2HTTPRequest) { r.Headers["authorization"] = "Basic invalid" }, 401},
		{"missing bridge token", func(a *application, r *events.APIGatewayV2HTTPRequest) { delete(r.Headers, "x-webdav-worker-token") }, 403},
		{"disabled", func(a *application, r *events.APIGatewayV2HTTPRequest) { a.workerToken = "" }, 404},
		{"wrong verb", func(a *application, r *events.APIGatewayV2HTTPRequest) { r.RequestContext.HTTP.Method = "PUT" }, 405},
		{"traversal", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = strings.Replace(r.Body, "ABCD1234.zip", "%2e%2e/other.zip", 1)
		}, 400},
		{"outside prefix", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = strings.Replace(r.Body, "/zotero/", "/elsewhere/", 1)
		}, 400},
		{"too large metadata", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = strings.Repeat("x", bridgeBodyLimit*3)
		}, 413},
		{"collection", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = strings.Replace(r.Body, "ABCD1234.zip", "", 1)
		}, 409},
		{"negative length", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = strings.Replace(r.Body, "33554432", "-1", 1)
		}, 400},
		{"trailing JSON", func(a *application, r *events.APIGatewayV2HTTPRequest) { r.Body += "{}" }, 400},
		{"payload not allowed", func(a *application, r *events.APIGatewayV2HTTPRequest) {
			r.Body = `{"path":"/zotero/a.zip","body":"file bytes","contentLength":10,"contentType":"application/zip"}`
		}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, r := bridgeFixture()
			c.mutate(a, &r)
			res, err := a.handle(context.Background(), r)
			if err != nil || res.StatusCode != c.status {
				t.Fatalf("got %d want %d", res.StatusCode, c.status)
			}
		})
	}
}
func TestDAVBridgeReusesPROPFINDAndRejectsOtherMethods(t *testing.T) {
	a, r := bridgeFixture()
	r.RawPath = "/_worker/dav"
	r.Body = `{"path":"/zotero/","method":"PROPFIND","depth":"0","body":"<propfind/>"}`
	res, err := a.handle(context.Background(), r)
	if err != nil || res.StatusCode != 207 || !strings.Contains(res.Body, "/zotero/") {
		t.Fatalf("PROPFIND failed: %d", res.StatusCode)
	}
	r.Body = `{"path":"/zotero/","method":"DELETE"}`
	res, _ = a.handle(context.Background(), r)
	if res.StatusCode != 405 {
		t.Fatal("unexpected method accepted")
	}
}
