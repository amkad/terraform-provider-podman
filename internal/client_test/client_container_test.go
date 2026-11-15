package client

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/decafcode/terraform-provider-podman/internal/api"
	"github.com/decafcode/terraform-provider-podman/internal/client"
	"github.com/decafcode/terraform-provider-podman/internal/testutil"
	"gotest.tools/v3/assert"
)

func TestContainerArchive(t *testing.T) {
	c := &testutil.TestContainer{
		Id:   "1",
		Json: api.ContainerCreateJson{Name: "one"},
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	files := []struct {
		path    string
		content string
	}{
		{
			path:    "/tmp/file1",
			content: "First",
		},
		{
			path:    "/tmp/file2",
			content: "Second",
		},
	}

	err = f.ContainerArchive(t.Context(), c.Json.Name, func(w *tar.Writer) error {
		for _, file := range files {
			bytes := []byte(file.content)

			err := w.WriteHeader(&tar.Header{
				Name: file.path,
				Size: int64(len(bytes)),
			})

			if err != nil {
				return err
			}

			_, err = w.Write(bytes)

			if err != nil {
				return err
			}
		}

		return nil
	})

	assert.NilError(t, err)
	expect := make([]testutil.TestUpload, len(files))

	for i, file := range files {
		expect[i] = testutil.TestUpload{
			Path:    file.path,
			Content: base64.StdEncoding.EncodeToString([]byte(file.content)),
		}
	}

	snap, err := apiServer.CaptureContainer(c.Id)
	assert.NilError(t, err)
	assert.DeepEqual(t, snap.UploadLog, expect)
}

func TestContainerCreate(t *testing.T) {
	apiServer := &testutil.ApiServer{}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	container := &api.ContainerCreateJson{
		Image: "example.com/library/test:v1.0.0",
		Name:  "test",
	}

	result, err := f.ContainerCreate(t.Context(), container)
	assert.NilError(t, err)

	if result.Id == "" {
		t.Error("no id")
	}
}

func TestContainerDelete(t *testing.T) {
	c := &testutil.TestContainer{
		Id:   "1",
		Json: api.ContainerCreateJson{Name: "one"},
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	err = f.ContainerDelete(t.Context(), c.Json.Name)
	assert.NilError(t, err)

	_, err = f.ContainerInspect(t.Context(), c.Json.Name)
	assert.ErrorContains(t, err, "not found")
}

func TestContainerInspect(t *testing.T) {
	c1 := &testutil.TestContainer{
		Id:   "1",
		Json: api.ContainerCreateJson{Name: "one"},
	}

	c2 := &testutil.TestContainer{
		Id:   "2",
		Json: api.ContainerCreateJson{Name: "two"},
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c1, c2},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	actual, err := f.ContainerInspect(t.Context(), c2.Json.Name)
	assert.NilError(t, err)
	assert.Equal(t, c2.Json.Name, actual.Name)
}

func TestContainerRename(t *testing.T) {
	c := &testutil.TestContainer{
		Id:   "1",
		Json: api.ContainerCreateJson{Name: "before"},
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	err = f.ContainerRename(t.Context(), c.Id, "after")
	assert.NilError(t, err)

	result, err := f.ContainerInspect(t.Context(), c.Json.Name)
	assert.NilError(t, err)
	assert.Equal(t, result.Name, "after")
}

func TestContainerStart(t *testing.T) {
	c := &testutil.TestContainer{
		Id:      "1",
		Json:    api.ContainerCreateJson{Name: "one"},
		Running: false,
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	err = f.ContainerStart(t.Context(), c.Id)
	assert.NilError(t, err)

	result, err := apiServer.CaptureContainer(c.Json.Name)
	assert.NilError(t, err)
	assert.Equal(t, result.Running, true)
}

func TestContainerStop(t *testing.T) {
	c := &testutil.TestContainer{
		Id:      "1",
		Json:    api.ContainerCreateJson{Name: "one"},
		Running: true,
	}

	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{c},
	}

	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)

	defer f.Stop(t.Context())

	err = f.ContainerStop(t.Context(), c.Id)
	assert.NilError(t, err)

	result, err := apiServer.CaptureContainer(c.Json.Name)
	assert.NilError(t, err)
	assert.Equal(t, result.Running, false)
}

func TestContainerWait(t *testing.T) {
	for _, exitCode := range []int32{0, 17} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			container := &testutil.TestContainer{
				Id:      "1",
				Json:    api.ContainerCreateJson{Name: "test"},
				Running: true,
			}
			started := make(chan struct{})
			completed := make(chan struct{}, 1)
			apiServer := &testutil.ApiServer{
				Containers: []*testutil.TestContainer{container},
				ContainerWait: func(ctx context.Context, _ string) (int32, error) {
					close(started)

					select {
					case <-completed:
						return exitCode, nil
					case <-ctx.Done():
						return 0, ctx.Err()
					}
				},
			}
			f, err := spawnFramework(t.Context(), apiServer)
			assert.NilError(t, err)
			defer f.Stop(t.Context())
			defer close(completed)

			type result struct {
				exitCode int32
				err      error
			}
			resultCh := make(chan result, 1)

			go func() {
				code, err := f.ContainerWait(t.Context(), container.Json.Name)
				resultCh <- result{exitCode: code, err: err}
			}()

			select {
			case <-started:
			case result := <-resultCh:
				t.Fatalf("wait returned before waiting for execution: %+v", result)
			}

			select {
			case result := <-resultCh:
				t.Fatalf("wait returned before execution completed: %+v", result)
			default:
			}

			completed <- struct{}{}
			actual := <-resultCh
			assert.NilError(t, actual.err)
			assert.Equal(t, actual.exitCode, exitCode)

			capture, err := apiServer.CaptureContainer(container.Id)
			assert.NilError(t, err)
			assert.Equal(t, capture.Running, false)
		})
	}
}

func TestContainerWaitCancellation(t *testing.T) {
	started := make(chan struct{})
	completed := make(chan struct{})
	apiServer := &testutil.ApiServer{
		Containers: []*testutil.TestContainer{{Id: "1", Running: true}},
		ContainerWait: func(ctx context.Context, _ string) (int32, error) {
			close(started)

			select {
			case <-completed:
				return 0, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		},
	}
	f, err := spawnFramework(t.Context(), apiServer)
	assert.NilError(t, err)
	defer f.Stop(t.Context())
	defer close(completed)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)

	go func() {
		_, err := f.ContainerWait(ctx, "1")
		result <- err
	}()

	select {
	case <-started:
	case err := <-result:
		t.Fatalf("wait returned before cancellation: %v", err)
	}
	cancel()
	assert.ErrorIs(t, <-result, context.Canceled)
}

func TestContainerWaitErrors(t *testing.T) {
	testCases := []struct {
		name   string
		status int
		body   string
		error  string
	}{
		{name: "not_found", status: http.StatusNotFound, body: "container not found", error: "container not found"},
		{name: "server_error", status: http.StatusInternalServerError, body: "wait failed", error: "wait failed"},
		{name: "invalid_exit_code", status: http.StatusOK, body: "invalid", error: "invalid syntax"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
				assert.Equal(t, req.Method, http.MethodPost)
				assert.Equal(t, req.URL.Path, "/subpath/v5.0.0/libpod/containers/test/wait")
				resp.Header().Set("content-type", "text/plain")
				resp.WriteHeader(testCase.status)
				_, err := io.WriteString(resp, testCase.body)
				assert.NilError(t, err)
			}))
			defer server.Close()

			clientUrl, err := url.Parse(strings.Replace(server.URL, "http://", "tcp://", 1) + "/subpath/")
			assert.NilError(t, err)
			c, err := client.Connect(t.Context(), clientUrl, nil)
			assert.NilError(t, err)
			defer c.Close()

			_, err = c.ContainerWait(t.Context(), "test")
			assert.ErrorContains(t, err, testCase.error)
		})
	}
}
