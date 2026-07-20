package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const allowedMethods = "OPTIONS, PROPFIND, HEAD, GET, PUT, DELETE, MKCOL"

type application struct {
	bucket          string
	prefix          string
	username        string
	password        string
	presignLifetime time.Duration

	s3      *s3.Client
	presign *s3.PresignClient
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (writer *statusResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}

	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusResponseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}

	written, err := writer.ResponseWriter.Write(body)
	writer.bytes += int64(written)
	return written, err
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

	address := strings.TrimSpace(os.Getenv("HTTP_ADDRESS"))
	if address == "" {
		address = ":8080"
	}

	client := s3.NewFromConfig(cfg)

	app := &application{
		bucket:          bucket,
		prefix:          prefix,
		username:        username,
		password:        password,
		presignLifetime: time.Duration(expiration) * time.Second,
		s3:              client,
		presign:         s3.NewPresignClient(client),
	}

	server := &http.Server{
		Addr:              address,
		Handler:           app,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("webdav server listening address=%s prefix=/%s bucket=%s", address, prefix, bucket)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	case <-shutdownCtx.Done():
		log.Printf("shutdown signal received")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Fatalf("graceful shutdown failed: %v", err)
		}

		log.Printf("shutdown complete")
	}
}

func (app *application) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	start := time.Now()
	writer := &statusResponseWriter{ResponseWriter: w}

	app.handle(writer, request)

	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}

	log.Printf(
		"request method=%s path=%q status=%d duration=%s request_content_length=%d response_content_length=%d",
		request.Method,
		request.URL.EscapedPath(),
		status,
		time.Since(start),
		request.ContentLength,
		writer.bytes,
	)
}

func (app *application) handle(w http.ResponseWriter, request *http.Request) {
	method := strings.ToUpper(request.Method)
	rawPath := request.URL.EscapedPath()
	if rawPath == "" {
		rawPath = "/"
	}

	if !app.authorized(request.Header) {
		writeResponse(request, w, http.StatusUnauthorized, map[string]string{
			"WWW-Authenticate": `Basic realm="Zotero WebDAV", charset="UTF-8"`,
			"Content-Type":     "text/plain; charset=utf-8",
		}, "Authentication required.\n")
		return
	}

	key, isCollection, err := app.resolvePath(rawPath)
	if err != nil {
		writeTextResponse(request, w, http.StatusBadRequest, "Invalid WebDAV path.\n")
		return
	}

	switch method {
	case http.MethodOptions:
		app.handleOptions(w, request)
	case "PROPFIND":
		app.handlePropfind(w, request, key, isCollection)
	case http.MethodHead:
		app.handleHead(w, request, key, isCollection)
	case http.MethodGet:
		app.handleGet(w, request, key, isCollection)
	case http.MethodPut:
		app.handlePut(w, request, key, isCollection)
	case http.MethodDelete:
		app.handleDelete(w, request, key, isCollection)
	case "MKCOL":
		app.handleMkcol(w, request, key, isCollection)
	default:
		writeResponse(request, w, http.StatusMethodNotAllowed, map[string]string{
			"Allow": allowedMethods,
			"DAV":   "1",
		}, "")
	}
}

func (app *application) handleOptions(w http.ResponseWriter, request *http.Request) {
	writeResponse(request, w, http.StatusOK, map[string]string{
		"Allow":         allowedMethods,
		"DAV":           "1",
		"MS-Author-Via": "DAV",
	}, "")
}

func (app *application) handlePropfind(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()
	depth := strings.TrimSpace(getHeader(request.Header, "depth"))
	if depth == "" {
		depth = "1"
	}

	if depth != "0" && depth != "1" {
		writeResponse(request, w, http.StatusForbidden, map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
		}, "Only Depth 0 and Depth 1 are supported.\n")
		return
	}

	var responses []davResponse

	if isCollection {
		exists, err := app.collectionExists(ctx, key)
		if err != nil {
			log.Printf("PROPFIND collection check failed: %v", err)
			writeServerError(request, w)
			return
		}
		if !exists {
			writeTextResponse(request, w, http.StatusNotFound, "Collection not found.\n")
			return
		}

		responses = append(responses, collectionDAVResponse(app.hrefForKey(key)))

		if depth == "1" {
			children, err := app.listCollection(ctx, key)
			if err != nil {
				log.Printf("PROPFIND list failed: %v", err)
				writeServerError(request, w)
				return
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
				writeTextResponse(request, w, http.StatusNotFound, "Object not found.\n")
				return
			}

			log.Printf("PROPFIND HeadObject failed: %v", err)
			writeServerError(request, w)
			return
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
		writeServerError(request, w)
		return
	}

	xmlBody := xml.Header + string(body) + "\n"

	writeResponse(request, w, http.StatusMultiStatus, map[string]string{
		"Content-Type": `application/xml; charset="utf-8"`,
		"DAV":          "1",
	}, xmlBody)
}

func (app *application) handleHead(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()

	if isCollection {
		exists, err := app.collectionExists(ctx, key)
		if err != nil {
			log.Printf("HEAD collection check failed: %v", err)
			writeServerError(request, w)
			return
		}
		if !exists {
			writeTextResponse(request, w, http.StatusNotFound, "")
			return
		}

		writeResponse(request, w, http.StatusOK, map[string]string{
			"Content-Type": "httpd/unix-directory",
			"DAV":          "1",
		}, "")
		return
	}

	head, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			writeTextResponse(request, w, http.StatusNotFound, "")
			return
		}

		log.Printf("HEAD HeadObject failed: %v", err)
		writeServerError(request, w)
		return
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
		headers["Last-Modified"] = head.LastModified.UTC().Format(http.TimeFormat)
	}

	writeResponse(request, w, http.StatusOK, headers, "")
}

func (app *application) handleGet(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()

	if isCollection {
		writeTextResponse(request, w, http.StatusMethodNotAllowed, "Collections cannot be downloaded.\n")
		return
	}

	_, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			writeTextResponse(request, w, http.StatusNotFound, "Object not found.\n")
			return
		}

		log.Printf("GET HeadObject failed: %v", err)
		writeServerError(request, w)
		return
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
		writeServerError(request, w)
		return
	}

	writeResponse(request, w, http.StatusTemporaryRedirect, map[string]string{
		"Location":      presigned.URL,
		"Cache-Control": "no-store",
		"Content-Type":  "text/plain; charset=utf-8",
	}, "Temporary redirect.\n")
}

func (app *application) handlePut(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()

	if isCollection || key == app.rootKey() {
		writeTextResponse(request, w, http.StatusConflict, "PUT requires an object path.\n")
		return
	}

	existed := true

	_, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			existed = false
		} else {
			log.Printf("PUT existence check failed: %v", err)
			writeServerError(request, w)
			return
		}
	}

	contentType := strings.TrimSpace(getHeader(request.Header, "content-type"))
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(key))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = app.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(app.bucket),
		Key:           aws.String(key),
		Body:          request.Body,
		ContentLength: aws.Int64(request.ContentLength),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		log.Printf("PUT PutObject failed: %v", err)
		writeServerError(request, w)
		return
	}

	if existed {
		writeResponse(request, w, http.StatusNoContent, nil, "")
		return
	}

	writeResponse(request, w, http.StatusCreated, nil, "")
}

func (app *application) handleDelete(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()

	if key == app.rootKey() {
		writeTextResponse(request, w, http.StatusForbidden, "The Zotero root collection cannot be deleted.\n")
		return
	}

	if isCollection {
		children, err := app.listKeys(ctx, key)
		if err != nil {
			log.Printf("DELETE list collection failed: %v", err)
			writeServerError(request, w)
			return
		}

		if len(children) == 0 {
			writeTextResponse(request, w, http.StatusNotFound, "Collection not found.\n")
			return
		}

		for _, childKey := range children {
			_, err := app.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(app.bucket),
				Key:    aws.String(childKey),
			})
			if err != nil {
				log.Printf("DELETE child object failed key=%q: %v", childKey, err)
				writeServerError(request, w)
				return
			}
		}

		writeResponse(request, w, http.StatusNoContent, nil, "")
		return
	}

	_, err := app.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			writeTextResponse(request, w, http.StatusNotFound, "Object not found.\n")
			return
		}

		log.Printf("DELETE HeadObject failed: %v", err)
		writeServerError(request, w)
		return
	}

	_, err = app.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(app.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.Printf("DELETE DeleteObject failed: %v", err)
		writeServerError(request, w)
		return
	}

	writeResponse(request, w, http.StatusNoContent, nil, "")
}

func (app *application) handleMkcol(
	w http.ResponseWriter,
	request *http.Request,
	key string,
	isCollection bool,
) {
	ctx := request.Context()

	if !isCollection {
		key += "/"
	}

	exists, err := app.collectionExists(ctx, key)
	if err != nil {
		log.Printf("MKCOL collection check failed: %v", err)
		writeServerError(request, w)
		return
	}
	if exists {
		writeTextResponse(request, w, http.StatusMethodNotAllowed, "Collection already exists.\n")
		return
	}

	_, err = app.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(app.bucket),
		Key:           aws.String(key),
		Body:          http.NoBody,
		ContentLength: aws.Int64(0),
		ContentType:   aws.String("application/x-directory"),
	})
	if err != nil {
		log.Printf("MKCOL PutObject failed: %v", err)
		writeServerError(request, w)
		return
	}

	writeResponse(request, w, http.StatusCreated, nil, "")
}

func (app *application) authorized(headers http.Header) bool {
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
		modified = lastModified.UTC().Format(http.TimeFormat)
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

func getHeader(headers http.Header, wanted string) string {
	for name, values := range headers {
		if !strings.EqualFold(name, wanted) {
			continue
		}

		if len(values) == 0 {
			return ""
		}

		return values[0]
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

func writeResponse(
	request *http.Request,
	w http.ResponseWriter,
	status int,
	headers map[string]string,
	body string,
) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}

	if w.Header().Get("Content-Length") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len([]byte(body))))
	}

	w.WriteHeader(status)

	if request.Method == http.MethodHead || body == "" {
		return
	}

	if _, err := w.Write([]byte(body)); err != nil {
		log.Printf("write response body failed: %v", err)
	}
}

func writeTextResponse(
	request *http.Request,
	w http.ResponseWriter,
	status int,
	body string,
) {
	writeResponse(request, w, status, map[string]string{
		"Content-Type": "text/plain; charset=utf-8",
	}, body)
}

func writeServerError(request *http.Request, w http.ResponseWriter) {
	writeTextResponse(
		request,
		w,
		http.StatusInternalServerError,
		fmt.Sprintf("Internal WebDAV server error at %s.\n", time.Now().UTC().Format(time.RFC3339)),
	)
}
