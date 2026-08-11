package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunValidatesEnvironmentAndCommand(t *testing.T) {
	for _, tc := range []struct {
		name, url, token, want string
		args                   []string
	}{
		{"url", "", "token", "VIGILANTE_PROXY_URL", []string{"repo", "view"}},
		{"token", "http://example.test", "", "VIGILANTE_SANDBOX_TOKEN", []string{"repo", "view"}},
		{"command", "http://example.test", "token", "usage: gh", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VIGILANTE_PROXY_URL", tc.url)
			t.Setenv("VIGILANTE_SANDBOX_TOKEN", tc.token)
			var stderr bytes.Buffer
			if code := run(tc.args, strings.NewReader(""), io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("run()=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestRunForwardsCommandInputAndResponse(t *testing.T) {
	var got ghRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sandbox/gh" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ghResponse{ExitCode: 7, Stdout: "out", Stderr: "warn"})
	}))
	defer server.Close()
	t.Setenv("VIGILANTE_PROXY_URL", server.URL)
	t.Setenv("VIGILANTE_SANDBOX_TOKEN", "secret")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"issue", "comment", "42"}, strings.NewReader("body"), &stdout, &stderr); code != 7 {
		t.Fatalf("code=%d", code)
	}
	if got.Command != "issue comment 42" || got.Token != "secret" || got.Stdin != "body" {
		t.Fatalf("request=%#v", got)
	}
	if stdout.String() != "out" || stderr.String() != "warn" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunReportsInputTransportAndInvalidResponseErrors(t *testing.T) {
	t.Setenv("VIGILANTE_PROXY_URL", "http://127.0.0.1:1")
	t.Setenv("VIGILANTE_SANDBOX_TOKEN", "secret")
	var stderr bytes.Buffer
	if code := run([]string{"repo", "view"}, failingReader{}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "read stdin") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"repo", "view"}, strings.NewReader(""), io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "proxy request failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not-json") }))
	defer server.Close()
	t.Setenv("VIGILANTE_PROXY_URL", server.URL)
	stderr.Reset()
	if code := run([]string{"repo", "view"}, strings.NewReader(""), io.Discard, &stderr); code != 1 || stderr.String() != "not-json" {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
