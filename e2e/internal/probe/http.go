package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRequestTimeout = 10 * time.Second
	defaultPollInterval   = 2 * time.Second
	defaultHTTPPort       = 80
)

type ClientOptions struct {
	HTTPClient     *http.Client
	RequestTimeout time.Duration
}

type WaitOptions struct {
	PollInterval time.Duration
}

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

type EchoResponse struct {
	RequestHeaders http.Header `json:"requestHeaders"`
	RequestBody    string      `json:"requestBody"`
	RequestMethod  string      `json:"requestMethod"`
	RequestURL     string      `json:"requestURL"`
	Host           string      `json:"host"`
}

type Response struct {
	URL           string
	StatusCode    int
	Header        http.Header
	Body          []byte
	Echo          *EchoResponse
	EchoDecodeErr error
}

func NewClient(publicIP string, port int, opts *ClientOptions) (*Client, error) {
	publicIP = strings.TrimSpace(publicIP)
	if publicIP == "" {
		return nil, errors.New("public ip is required")
	}

	if port <= 0 {
		return nil, errors.New("port must be greater than zero")
	}

	if opts == nil {
		opts = &ClientOptions{}
	}

	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}

	return &Client{
		baseURL: &url.URL{
			Scheme: "http",
			Host:   makeHost(publicIP, port),
		},
		httpClient: httpClient,
	}, nil
}

func (c *Client) Host() string {
	if c == nil || c.baseURL == nil {
		return ""
	}

	return c.baseURL.Host
}

func (c *Client) Probe(ctx context.Context, path string) (*Response, error) {
	target, err := c.url(path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build probe request for %q: %w", target, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe %q: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read probe response from %q: %w", target, err)
	}

	result := &Response{
		URL:        target,
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       body,
	}

	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return result, nil
	}

	var echo EchoResponse
	if decodeErr := json.Unmarshal(body, &echo); decodeErr != nil {
		result.EchoDecodeErr = decodeErr
		return result, nil
	}

	result.Echo = &echo

	return result, nil
}

func WaitForEcho(ctx context.Context, client *Client, path string, opts *WaitOptions) (*Response, error) {
	expectedHost := client.Host()

	return waitFor(
		ctx,
		opts,
		fmt.Sprintf("wait for HTTP echo response on %q", path),
		func(ctx context.Context) (bool, *Response, string) {
			response, probeErr := client.Probe(ctx, path)
			if probeErr != nil {
				return false, nil, probeErr.Error()
			}

			if response.IsExpectedEcho(path, expectedHost) {
				return true, response, ""
			}

			return false, response, response.describeExpectation(path, expectedHost)
		},
	)
}

func WaitForEchoGone(
	ctx context.Context,
	client *Client,
	path string,
	opts *WaitOptions,
) (*Response, error) {
	expectedHost := client.Host()

	return waitFor(
		ctx,
		opts,
		fmt.Sprintf("wait for HTTP echo removal on %q", path),
		func(ctx context.Context) (bool, *Response, string) {
			response, probeErr := client.Probe(ctx, path)
			if probeErr != nil {
				return false, nil, probeErr.Error()
			}

			if !response.IsExpectedEcho(path, expectedHost) {
				return true, response, ""
			}

			return false, response, "expected echo response is still being served"
		},
	)
}

func (r *Response) IsExpectedEcho(requestURL string, host string) bool {
	if r == nil || r.StatusCode != http.StatusOK || r.Echo == nil || r.EchoDecodeErr != nil {
		return false
	}

	return r.Echo.RequestMethod == http.MethodGet &&
		r.Echo.RequestBody == "" &&
		r.Echo.RequestURL == requestURL &&
		r.Echo.Host == host
}

func (r *Response) describeExpectation(requestURL string, host string) string {
	if r == nil {
		return "no response received"
	}

	switch {
	case r.StatusCode != http.StatusOK:
		return fmt.Sprintf("received status %d", r.StatusCode)
	case r.EchoDecodeErr != nil:
		return fmt.Sprintf("failed to decode echo json: %v", r.EchoDecodeErr)
	case r.Echo == nil:
		return "response body was empty"
	case r.Echo.RequestMethod != http.MethodGet:
		return fmt.Sprintf("echo method mismatch: got %q", r.Echo.RequestMethod)
	case r.Echo.RequestBody != "":
		return "echo body was not empty"
	case r.Echo.RequestURL != requestURL:
		return fmt.Sprintf("echo request url mismatch: got %q", r.Echo.RequestURL)
	case r.Echo.Host != host:
		return fmt.Sprintf("echo host mismatch: got %q", r.Echo.Host)
	default:
		return "response did not match expected echo shape"
	}
}

func (c *Client) url(path string) (string, error) {
	if c == nil || c.baseURL == nil {
		return "", errors.New("probe client is not initialized")
	}

	ref, err := normalizePath(path)
	if err != nil {
		return "", err
	}

	return c.baseURL.ResolveReference(ref).String(), nil
}

func normalizePath(path string) (*url.URL, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("parse path %q: %w", path, err)
	}

	return ref, nil
}

func makeHost(publicIP string, port int) string {
	if port == defaultHTTPPort {
		if strings.Contains(publicIP, ":") && !strings.HasPrefix(publicIP, "[") {
			return "[" + publicIP + "]"
		}

		return publicIP
	}

	return net.JoinHostPort(publicIP, strconv.Itoa(port))
}

func waitFor(
	ctx context.Context,
	opts *WaitOptions,
	description string,
	check func(context.Context) (bool, *Response, string),
) (*Response, error) {
	pollInterval := defaultPollInterval
	if opts != nil && opts.PollInterval > 0 {
		pollInterval = opts.PollInterval
	}

	var lastMessage string
	var lastResponse *Response

	for {
		done, response, message := check(ctx)
		if done {
			return response, nil
		}

		lastResponse = response
		lastMessage = message

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastMessage != "" {
				return lastResponse, fmt.Errorf("%s: %s: %w", description, lastMessage, ctx.Err())
			}

			return lastResponse, fmt.Errorf("%s: %w", description, ctx.Err())
		case <-timer.C:
		}
	}
}
