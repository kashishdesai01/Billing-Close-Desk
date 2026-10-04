package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strings"
)

type ERPClient struct {
	Identity   oauth2.TokenSource
	URL, Token string
	Client     *http.Client
}

func (e *ERPClient) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(e.URL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	req.Header.Set("Content-Type", "application/json")
	if e.Identity != nil {
		t, err := e.Identity.Token()
		if err != nil {
			return err
		}
		req.Header.Set("X-Serverless-Authorization", "Bearer "+t.AccessToken)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ERP status %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return err
}
func (e *ERPClient) Put(ctx context.Context, d Document) error {
	return e.call(ctx, "PUT", "/records", d, nil)
}
func (e *ERPClient) List(ctx context.Context) ([]Document, error) {
	var docs []Document
	err := e.call(ctx, "GET", "/records", nil, &docs)
	return docs, err
}
func (e *ERPClient) Control(ctx context.Context, mode, id string) error {
	return e.call(ctx, "POST", "/control", map[string]string{"mode": mode, "id": id}, nil)
}
