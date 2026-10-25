package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type application struct {
	bucket             string
	prefix             string
	username           string
	password           string
	presignLifetime    time.Duration
	putPresignLifetime time.Duration
	workerToken        string

	s3      *s3.Client
	presign *s3.PresignClient
}

type multistatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	XmlnsD    string        `xml:"xmlns:D,attr"`
	Responses []davResponse `xml:"D:response"`
}

type davResponse struct {
	Href     string   `xml:"D:href"`
	Propstat propstat `xml:"D:propstat"`
}

type propstat struct {
	Prop   davProperties `xml:"D:prop"`
	Status string        `xml:"D:status"`
}

type davProperties struct {
	DisplayName     string        `xml:"D:displayname"`
	ResourceType    *resourceType `xml:"D:resourcetype"`
	ContentLength   *int64        `xml:"D:getcontentlength,omitempty"`
	ContentType     string        `xml:"D:getcontenttype,omitempty"`
	LastModified    string        `xml:"D:getlastmodified,omitempty"`
	EntityTag       string        `xml:"D:getetag,omitempty"`
	CreationDate    string        `xml:"D:creationdate,omitempty"`
	ContentLanguage string        `xml:"D:getcontentlanguage,omitempty"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

func main() {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}

	bucket := strings.TrimSpace(os.Getenv("LIBRARY_BUCKET"))
	username := os.Getenv("WEBDAV_USERNAME")
	password := os.Getenv("WEBDAV_PASSWORD")

	if bucket == "" {
		log.Fatal("LIBRARY_BUCKET is required")
	}
	if username == "" {
		log.Fatal("WEBDAV_USERNAME is required")
	}
	if password == "" {
		log.Fatal("WEBDAV_PASSWORD is required")
	}

	prefix := strings.Trim(strings.TrimSpace(os.Getenv("WEBDAV_PREFIX")), "/")
	if prefix == "" {
		prefix = "zotero"
	}

	expiration := envInt("PRESIGNED_GET_EXPIRATION", 300)
	if expiration < 1 {
		expiration = 300
	}

	putExpiration := envInt("PRESIGNED_PUT_EXPIRATION", 120)
	if putExpiration < 1 || putExpiration > 300 {
		putExpiration = 120
	}

	client := s3.NewFromConfig(cfg)

	app := &application{
		bucket:             bucket,
		prefix:             prefix,
		username:           username,
		password:           password,
		presignLifetime:    time.Duration(expiration) * time.Second,
		putPresignLifetime: time.Duration(putExpiration) * time.Second,
		workerToken:        os.Getenv("WEBDAV_WORKER_TOKEN"),
		s3:                 client,
		presign:            s3.NewPresignClient(client),
	}

	lambda.Start(app.handle)
}

func (app *application) handle(
	ctx context.Context,
	request events.APIGatewayV2HTTPRequest,
) (events.APIGatewayV2HTTPResponse, error) {
	method := strings.ToUpper(request.RequestContext.HTTP.Method)
	rawPath := request.RawPath

	if rawPath == "" {
		rawPath = request.RequestContext.HTTP.Path
	}

	log.Printf(
		"request method=%s path=%q request_id=%q",
		method,
		rawPath,
		request.RequestContext.RequestID,
	)

	if !app.authorized(request.Headers) {
		return response(401, map[string]string{
			"WWW-Authenticate": `Basic realm="Zotero WebDAV", charset="UTF-8"`,
			"Content-Type":     "text/plain; charset=utf-8",
		}, "Authentication required.\n"), nil
	}

	// The bridge accepts only small metadata, never the original upload body.
	if rawPath == "/_worker/upload" || rawPath == "/_worker/dav" {
		return app.handleWorkerBridge(ctx, request, rawPath)
	}

	key, isCollection, err := app.resolvePath(rawPath)
	if err != nil {
		return textResponse(400, "Invalid WebDAV path.\n"), nil
	}

	switch method {
	case "OPTIONS":
		return app.handleOptions(), nil

	case "PROPFIND":
		return app.handlePropfind(ctx, request, key, isCollection)

	case "HEAD":
		return app.handleHead(ctx, key, isCollection)

	case "GET":
		return app.handleGet(ctx, key, isCollection)

	case "PUT":
		return app.handlePut(ctx, request, key, isCollection)

	case "DELETE":
		return app.handleDelete(ctx, key, isCollection)

	case "MKCOL":
		return app.handleMkcol(ctx, key, isCollection)

	default:
		return response(405, map[string]string{
			"Allow": "OPTIONS, PROPFIND, HEAD, GET, PUT, DELETE, MKCOL",
			"DAV":   "1",
		}, ""), nil
	}
}

func (app *application) handleOptions() events.APIGatewayV2HTTPResponse {
	return response(200, map[string]string{
		"Allow":          "OPTIONS, PROPFIND, HEAD, GET, PUT, DELETE, MKCOL",
		"DAV":            "1",
		"MS-Author-Via":  "DAV",
		"Content-Length": "0",
	}, "")
}

func (app *application) handlePropfind(
	ctx context.Context,
	request events.APIGatewayV2HTTPRequest,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	depth := strings.TrimSpace(getHeader(request.Headers, "depth"))
	if depth == "" {
		depth = "1"
	}

	if depth != "0" && depth != "1" {
		return response(403, map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
		}, "Only Depth 0 and Depth 1 are supported.\n"), nil
	}

	var responses []davResponse

	if isCollection {
		exists, err := app.collectionExists(ctx, key)
		if err != nil {
			log.Printf("PROPFIND collection check failed: %v", err)
			return serverError(), nil
		}
		if !exists {
			return textResponse(404, "Collection not found.\n"), nil
		}

		responses = append(responses, collectionDAVResponse(app.hrefForKey(key)))

		if depth == "1" {
			children, err := app.listCollection(ctx, key)
			if err != nil {
				log.Printf("PROPFIND list failed: %v", err)
				return serverError(), nil
			}
			responses = append(responses, children...)
		}
	} else {
		head, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(app.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			if isNotFound(err) {
				return textResponse(404, "Object not found.\n"), nil
			}

			log.Printf("PROPFIND HeadObject failed: %v", err)
			return serverError(), nil
		}

		responses = append(responses, objectDAVResponse(
			app.hrefForKey(key),
			key,
			aws.ToInt64(head.ContentLength),
			aws.ToString(head.ContentType),
			aws.ToString(head.ETag),
			head.LastModified,
		))
	}

	document := multistatus{
		XmlnsD:    "DAV:",
		Responses: responses,
	}

	body, err := xml.MarshalIndent(document, "", "  ")
	if err != nil {
		log.Printf("marshal PROPFIND XML: %v", err)
		return serverError(), nil
	}

	xmlBody := xml.Header + string(body) + "\n"

	return response(207, map[string]string{
		"Content-Type":   `application/xml; charset="utf-8"`,
		"DAV":            "1",
		"Content-Length": strconv.Itoa(len([]byte(xmlBody))),
	}, xmlBody), nil
}

func (app *application) handleHead(
	ctx context.Context,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	if isCollection {
		exists, err := app.collectionExists(ctx, key)
		if err != nil {
			log.Printf("HEAD collection check failed: %v", err)
			return serverError(), nil
		}
		if !exists {
			return textResponse(404, ""), nil
		}

		return response(200, map[string]string{
			"Content-Type":   "httpd/unix-directory",
			"Content-Length": "0",
			"DAV":            "1",
		}, ""), nil
	}

	head, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return textResponse(404, ""), nil
		}

		log.Printf("HEAD HeadObject failed: %v", err)
		return serverError(), nil
	}

	headers := map[string]string{
		"Content-Length": strconv.FormatInt(aws.ToInt64(head.ContentLength), 10),
		"Accept-Ranges":  "bytes",
	}

	if value := aws.ToString(head.ContentType); value != "" {
		headers["Content-Type"] = value
	}

	if value := aws.ToString(head.ETag); value != "" {
		headers["ETag"] = value
	}

	if head.LastModified != nil {
		headers["Last-Modified"] = head.LastModified.UTC().Format(time.RFC1123)
	}

	return response(200, headers, ""), nil
}

func (app *application) handleGet(
	ctx context.Context,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	if isCollection {
		return textResponse(405, "Collections cannot be downloaded.\n"), nil
	}

	_, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return textResponse(404, "Object not found.\n"), nil
		}

		log.Printf("GET HeadObject failed: %v", err)
		return serverError(), nil
	}

	presigned, err := app.presign.PresignGetObject(
		ctx,
		&s3.GetObjectInput{
			Bucket: aws.String(app.bucket),
			Key:    aws.String(key),
		},
		func(options *s3.PresignOptions) {
			options.Expires = app.presignLifetime
		},
	)
	if err != nil {
		log.Printf("presign GET failed: %v", err)
		return serverError(), nil
	}

	return response(307, map[string]string{
		"Location":      presigned.URL,
		"Cache-Control": "no-store",
		"Content-Type":  "text/plain; charset=utf-8",
	}, "Temporary redirect.\n"), nil
}

func (app *application) handlePut(
	ctx context.Context,
	request events.APIGatewayV2HTTPRequest,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	if isCollection || key == app.rootKey() {
		return textResponse(409, "PUT requires an object path.\n"), nil
	}

	body, err := requestBody(request)
	if err != nil {
		return textResponse(400, "Invalid request body.\n"), nil
	}

	existed := true

	_, err = app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			existed = false
		} else {
			log.Printf("PUT existence check failed: %v", err)
			return serverError(), nil
		}
	}

	contentType := strings.TrimSpace(getHeader(request.Headers, "content-type"))
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(key))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = app.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(app.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		log.Printf("PUT PutObject failed: %v", err)
		return serverError(), nil
	}

	if existed {
		return response(204, map[string]string{
			"Content-Length": "0",
		}, ""), nil
	}

	return response(201, map[string]string{
		"Content-Length": "0",
	}, ""), nil
}

func (app *application) handleDelete(
	ctx context.Context,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	if key == app.rootKey() {
		return textResponse(403, "The Zotero root collection cannot be deleted.\n"), nil
	}

	if isCollection {
		children, err := app.listKeys(ctx, key)
		if err != nil {
			log.Printf("DELETE list collection failed: %v", err)
			return serverError(), nil
		}

		if len(children) == 0 {
			return textResponse(404, "Collection not found.\n"), nil
		}

		for _, childKey := range children {
			_, err := app.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(app.bucket),
				Key:    aws.String(childKey),
			})
			if err != nil {
				log.Printf("DELETE child object failed key=%q: %v", childKey, err)
				return serverError(), nil
			}
		}

		return response(204, map[string]string{
			"Content-Length": "0",
		}, ""), nil
	}

	_, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return textResponse(404, "Object not found.\n"), nil
		}

		log.Printf("DELETE HeadObject failed: %v", err)
		return serverError(), nil
	}

	_, err = app.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.Printf("DELETE DeleteObject failed: %v", err)
		return serverError(), nil
	}

	return response(204, map[string]string{
		"Content-Length": "0",
	}, ""), nil
}

func (app *application) handleMkcol(
	ctx context.Context,
	key string,
	isCollection bool,
) (events.APIGatewayV2HTTPResponse, error) {
	if !isCollection {
		key += "/"
	}

	exists, err := app.collectionExists(ctx, key)
	if err != nil {
		log.Printf("MKCOL collection check failed: %v", err)
		return serverError(), nil
	}
	if exists {
		return textResponse(405, "Collection already exists.\n"), nil
	}

	_, err = app.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(app.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(nil),
		ContentLength: aws.Int64(0),
		ContentType:   aws.String("application/x-directory"),
	})
	if err != nil {
		log.Printf("MKCOL PutObject failed: %v", err)
		return serverError(), nil
	}

	return response(201, map[string]string{
		"Content-Length": "0",
	}, ""), nil
}

func (app *application) authorized(headers map[string]string) bool {
	header := strings.TrimSpace(getHeader(headers, "authorization"))
	if header == "" {
		return false
	}

	scheme, encoded, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return false
	}

	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}

	usernameMatch := subtle.ConstantTimeCompare(
		[]byte(username),
		[]byte(app.username),
	)
	passwordMatch := subtle.ConstantTimeCompare(
		[]byte(password),
		[]byte(app.password),
	)

	return usernameMatch == 1 && passwordMatch == 1
}

func (app *application) resolvePath(rawPath string) (string, bool, error) {
	if rawPath == "" {
		rawPath = "/"
	}

	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", false, err
	}

	if strings.Contains(decodedPath, "\x00") {
		return "", false, errors.New("path contains null byte")
	}

	cleaned := path.Clean("/" + strings.TrimSpace(decodedPath))
	expectedRoot := "/" + app.prefix

	if cleaned != expectedRoot &&
		cleaned != expectedRoot+"/" &&
		!strings.HasPrefix(cleaned, expectedRoot+"/") {
		return "", false, errors.New("path outside WebDAV root")
	}

	relative := strings.TrimPrefix(cleaned, expectedRoot)
	relative = strings.TrimPrefix(relative, "/")

	isCollection := strings.HasSuffix(decodedPath, "/") ||
		cleaned == expectedRoot

	if relative == "" {
		return app.rootKey(), true, nil
	}

	key := app.prefix + "/" + relative
	if isCollection {
		key = strings.TrimSuffix(key, "/") + "/"
	}

	return key, isCollection, nil
}

func (app *application) rootKey() string {
	return app.prefix + "/"
}

func (app *application) collectionExists(
	ctx context.Context,
	key string,
) (bool, error) {
	if key == app.rootKey() {
		return true, nil
	}

	output, err := app.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(app.bucket),
		Prefix:  aws.String(strings.TrimSuffix(key, "/") + "/"),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, err
	}

	return len(output.Contents) > 0, nil
}

func (app *application) listCollection(
	ctx context.Context,
	collectionKey string,
) ([]davResponse, error) {
	prefix := strings.TrimSuffix(collectionKey, "/") + "/"

	output, err := app.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(app.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, err
	}

	responses := make([]davResponse, 0, len(output.Contents)+len(output.CommonPrefixes))

	for _, commonPrefix := range output.CommonPrefixes {
		key := aws.ToString(commonPrefix.Prefix)
		if key == "" {
			continue
		}

		responses = append(
			responses,
			collectionDAVResponse(app.hrefForKey(key)),
		)
	}

	for _, object := range output.Contents {
		key := aws.ToString(object.Key)

		if key == "" || key == prefix || strings.HasSuffix(key, "/") {
			continue
		}

		contentType := mime.TypeByExtension(path.Ext(key))
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		responses = append(responses, objectDAVResponse(
			app.hrefForKey(key),
			key,
			aws.ToInt64(object.Size),
			contentType,
			aws.ToString(object.ETag),
			object.LastModified,
		))
	}

	return responses, nil
}

func (app *application) listKeys(
	ctx context.Context,
	collectionKey string,
) ([]string, error) {
	prefix := strings.TrimSuffix(collectionKey, "/") + "/"
	var keys []string
	var continuationToken *string

	for {
		output, err := app.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(app.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, err
		}

		for _, object := range output.Contents {
			key := aws.ToString(object.Key)
			if key != "" {
				keys = append(keys, key)
			}
		}

		if !aws.ToBool(output.IsTruncated) {
			break
		}

		continuationToken = output.NextContinuationToken
	}

	return keys, nil
}

func (app *application) hrefForKey(key string) string {
	relative := strings.TrimPrefix(key, app.prefix)
	if relative == "" {
		relative = "/"
	}

	segments := strings.Split(relative, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}

	href := "/" + app.prefix + strings.Join(segments, "/")

	if strings.HasSuffix(key, "/") && !strings.HasSuffix(href, "/") {
		href += "/"
	}

	return href
}

func collectionDAVResponse(href string) davResponse {
	return davResponse{
		Href: href,
		Propstat: propstat{
			Prop: davProperties{
				DisplayName: path.Base(strings.TrimSuffix(href, "/")),
				ResourceType: &resourceType{
					Collection: &struct{}{},
				},
			},
			Status: "HTTP/1.1 200 OK",
		},
	}
}

func objectDAVResponse(
	href string,
	key string,
	size int64,
	contentType string,
	etag string,
	lastModified *time.Time,
) davResponse {
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var modified string
	var created string

	if lastModified != nil {
		modified = lastModified.UTC().Format(time.RFC1123)
		created = lastModified.UTC().Format(time.RFC3339)
	}

	return davResponse{
		Href: href,
		Propstat: propstat{
			Prop: davProperties{
				DisplayName:   path.Base(key),
				ResourceType:  &resourceType{},
				ContentLength: aws.Int64(size),
				ContentType:   contentType,
				LastModified:  modified,
				CreationDate:  created,
				EntityTag:     etag,
			},
			Status: "HTTP/1.1 200 OK",
		},
	}
}

func requestBody(request events.APIGatewayV2HTTPRequest) ([]byte, error) {
	if request.Body == "" {
		return []byte{}, nil
	}

	if request.IsBase64Encoded {
		return base64.StdEncoding.DecodeString(request.Body)
	}

	return []byte(request.Body), nil
}

func getHeader(headers map[string]string, wanted string) string {
	for name, value := range headers {
		if strings.EqualFold(name, wanted) {
			return value
		}
	}

	return ""
}

func isNotFound(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}

	switch apiError.ErrorCode() {
	case "NotFound", "NoSuchKey", "NoSuchBucket":
		return true
	default:
		return false
	}
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func response(
	status int,
	headers map[string]string,
	body string,
) events.APIGatewayV2HTTPResponse {
	if headers == nil {
		headers = make(map[string]string)
	}

	if _, exists := headers["Content-Length"]; !exists {
		headers["Content-Length"] = strconv.Itoa(len([]byte(body)))
	}

	return events.APIGatewayV2HTTPResponse{
		StatusCode:      status,
		Headers:         headers,
		Body:            body,
		IsBase64Encoded: false,
	}
}

func textResponse(
	status int,
	body string,
) events.APIGatewayV2HTTPResponse {
	return response(status, map[string]string{
		"Content-Type": "text/plain; charset=utf-8",
	}, body)
}

func serverError() events.APIGatewayV2HTTPResponse {
	return textResponse(
		500,
		fmt.Sprintf("Internal WebDAV server error at %s.\n", time.Now().UTC().Format(time.RFC3339)),
	)
}
