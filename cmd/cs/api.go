package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// --- the API ---------------------------------------------------------------------------------

type mount struct {
	Path     string     `json:"path"`
	Volume   string     `json:"volume,omitempty"`
	Temp     bool       `json:"temp,omitempty"`
	ReadOnly bool       `json:"readOnly,omitempty"`
	SizeMiB  int        `json:"sizeMiB,omitempty"`
	New      *newVolume `json:"new,omitempty"`
}

type newVolume struct {
	StorageClass string `json:"storageClass"`
	SizeGiB      int    `json:"sizeGiB"`
}

type spec struct {
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	Mounts      []mount `json:"mounts"`
	CPUMillis   int     `json:"cpuMillis"`
	MemoryMiB   int     `json:"memoryMiB"`
	DiskGiB     int     `json:"diskGiB"`
	Debug       bool    `json:"debug"`
	ClaudeLogin bool    `json:"claudeLogin"`
}

type container struct {
	spec
	Running bool   `json:"running"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type session struct {
	Username string `json:"username"`
	SSH      *struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"ssh"`
	Images []string `json:"images"`
}

type apiError struct {
	status int
	detail string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (%d)", e.detail, e.status) }

// call does a request with the token, signing in first if there's none or it has expired.
func call(method, path string, in, out any) error {
	for attempt := 0; ; attempt++ {
		token := keychainGet()
		if token == "" {
			login()
			token = keychainGet()
		}
		var body io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, origin()+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			fmt.Fprintln(os.Stderr, "The sign-in has expired.")
			keychainDelete()
			continue
		}
		if resp.StatusCode >= 300 {
			var e struct{ Detail string }
			json.Unmarshal(data, &e)
			if e.Detail == "" {
				e.Detail = resp.Status
			}
			return &apiError{resp.StatusCode, e.Detail}
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func containers() []container {
	var got struct{ Containers []container }
	must(call("GET", "/api/containers", nil, &got))
	return got.Containers
}

func get(name string) container {
	var c container
	must(call("GET", "/api/containers/"+pathName(name), nil, &c))
	return c
}

// waitRunning waits for the container to run, on the server's change events (no polling).
func waitRunning(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin()+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+keychainGet())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("watching for the container: %v", err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	last := ""
	for {
		c := get(name)
		if c.State != last {
			fmt.Fprintf(os.Stderr, "  %s: %s %s\n", name, c.State, c.Message)
			last = c.State
		}
		switch {
		case c.State == "running":
			return
		case strings.Contains(c.State, "BackOff") || c.State == "ErrImagePull" || c.State == "CreateContainerConfigError":
			die("%s won't start: %s %s", name, c.State, c.Message)
		}
		// Next change (the stream also sends a comment now and then to stay open).
		for lines.Scan() && !strings.HasPrefix(lines.Text(), "event:") && !strings.HasPrefix(lines.Text(), "data:") {
		}
		if lines.Err() != nil || ctx.Err() != nil {
			die("%s didn't start within 5 minutes", name)
		}
	}
}

func pathName(name string) string { return url.PathEscape(name) }

// sessionInfo is who's signed in, the bastion and the suggested images (signing in if needed).
func sessionInfo() session {
	var s session
	must(call("GET", "/api/session", nil, &s))
	return s
}

// ensureRunning starts the container if it's stopped and waits for it to run; fresh: it wasn't.
func ensureRunning(name string) (fresh bool) {
	c := get(name)
	if !c.Running {
		fmt.Fprintf(os.Stderr, "Starting %s…\n", name)
		must(call("POST", "/api/containers/"+pathName(name)+"/start", nil, nil))
		fresh = true
	}
	if c.State != "running" {
		waitRunning(name)
		fresh = true
	}
	return fresh
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
