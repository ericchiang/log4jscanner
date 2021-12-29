// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// https://docs.github.com/en/actions/learn-github-actions/environment-variables#default-environment-variables
const (
	envActions = "GITHUB_ACTIONS"
	envAPIURL  = "GITHUB_API_URL"
	envRef     = "GITHUB_REF_NAME"
	envRepo    = "GITHUB_REPOSITORY"
	envToken   = "GITHUB_TOKEN"

	envActionsValTrue = "true"
)

const (
	contentTypeZIP   = "application/zip"
	contentTypeTARGZ = "application/gzip"
)

type client struct {
	baseURL *url.URL
	repo    string
	ref     string
	token   string
}

func (c *client) upload(ctx context.Context, uploadURL, path, contentType string) error {
	// https://docs.github.com/en/rest/reference/releases#upload-a-release-asset
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat asset: %v", err)
	}
	name := filepath.Base(path)

	u, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("parse url: %v", err)
	}
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), nil)
	if err != nil {
		return fmt.Errorf("creating request: %v", err)
	}
	req.Header.Set("Authorization", "token: "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = stat.Size()
	req.GetBody = func() (io.ReadCloser, error) {
		return os.Open(path)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		dump, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %s: %s: %s", u, resp.Status, dump)
	}
	return nil
}

func (c *client) uploadURL(ctx context.Context) (string, error) {
	// https://docs.github.com/en/rest/reference/releases#get-a-release-by-tag-name
	u, err := c.baseURL.Parse(path.Join("/repos", c.repo, "releases/tags", c.ref))
	if err != nil {
		return "", fmt.Errorf("parse url: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %v", err)
	}
	req.Header.Set("Authorization", "token: "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %s", resp.Status)
	}
	var body struct {
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding response: %v", err)
	}

	// String includes a templated query at the end "{?name,label}". Remove the
	// template.
	//
	// https://docs.github.com/en/rest/overview/resources-in-the-rest-api#hypermedia
	uploadURL := body.UploadURL
	if i := strings.Index(uploadURL, "{"); i >= 0 {
		uploadURL = uploadURL[:i]
	}

	return uploadURL, nil
}

func clientFromEnv(env func(s string) string) (*client, error) {
	if got := env(envActions); got != envActionsValTrue {
		return nil, fmt.Errorf("not running under github actions")
	}
	for _, key := range [...]string{
		envAPIURL,
		envRef,
		envRepo,
		envToken,
	} {
		if env(key) == "" {
			return nil, fmt.Errorf("expected environment variable not present: %s", key)
		}
	}

	u, err := url.Parse(env(envAPIURL))
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %v", err)
	}
	return &client{
		baseURL: u,
		repo:    env(envRepo),
		ref:     env(envRef),
		token:   env(envToken),
	}, nil
}

func main() {
	ctx := context.Background()
	c, err := clientFromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("creating client: %v", err)
	}
	url, err := c.uploadURL(ctx)
	if err != nil {
		log.Fatalf("creating upload url: %v", err)
	}

	const binDir = "./bin"
	entries, err := os.ReadDir(binDir)
	if err != nil {
		log.Fatalf("reading dir ./bin: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ct := ""
		if strings.HasSuffix(name, ".zip") {
			ct = contentTypeZIP
		} else if strings.HasSuffix(name, ".tar.gz") {
			ct = contentTypeTARGZ
		}
		if ct == "" {
			continue
		}
		if err := c.upload(ctx, url, filepath.Join(binDir, name), ct); err != nil {
			log.Fatalf("upload file %s: %v", name, err)
		}
	}
}
