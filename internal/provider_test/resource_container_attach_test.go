package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"testing"

	"github.com/decafcode/terraform-provider-podman/internal/testutil"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestAccContainerResourceAttach(t *testing.T) {
	testCases := []struct {
		name       string
		attributes string
		attach     string
		running    bool
	}{
		{name: "omitted", attach: "false", running: true},
		{name: "false", attributes: "attach = false", attach: "false", running: true},
		{name: "null", attributes: "attach = null", attach: "false", running: true},
		{name: "true", attributes: "attach = true", attach: "true"},
		{name: "null_start", attributes: "attach = true\nstart_immediately = null", attach: "true"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			apiServer := testutil.ApiServer{}
			framework, err := spawnFramework(t.Context(), &apiServer)
			assert.NilError(t, err)
			defer framework.Stop(t.Context())

			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: providerFactories,
				CheckDestroy: func(_ *terraform.State) error {
					return apiServer.ContainerWalk(func(c *testutil.TestContainer) error {
						return fmt.Errorf("leftover container: %s", c.Json.Name)
					})
				},
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
							resource "podman_container" "test" {
								container_host = "%s"
								image          = "example.com/library/test:v1.0.0"
								name           = "test"
								%s

								uploads = [{
									content      = "Hello"
									content_hash = "hash_of_hello"
									path         = "/tmp/file"
								}]
							}
						`, framework.Url(), testCase.attributes),
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr("podman_container.test", "attach", testCase.attach),
							func(_ *terraform.State) error {
								capture, err := apiServer.CaptureContainer("test")

								if err != nil {
									return err
								}

								if capture.Running != testCase.running {
									return fmt.Errorf("container running = %t, want %t", capture.Running, testCase.running)
								}

								result := cmp.DeepEqual(capture.UploadLog, []testutil.TestUpload{{
									Content: base64.StdEncoding.EncodeToString([]byte("Hello")),
									Mode:    0644,
									Path:    "/tmp/file",
								}})()

								if !result.Success() {
									t.Log(result)

									return fmt.Errorf("incorrect upload before execution")
								}

								return nil
							},
						),
					},
				},
			})
		})
	}
}

func TestAccContainerResourceAttachFailure(t *testing.T) {
	testCases := []struct {
		name     string
		exitCode int32
		waitErr  error
		error    string
	}{
		{name: "execution", exitCode: 17, error: `Container execution failed[\s\S]*Container 1 exited with code 17`},
		{name: "wait", waitErr: fmt.Errorf("wait failed"), error: "Container wait failed"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			apiServer := testutil.ApiServer{
				ContainerWait: func(context.Context, string) (int32, error) {
					return testCase.exitCode, testCase.waitErr
				},
			}
			framework, err := spawnFramework(t.Context(), &apiServer)
			assert.NilError(t, err)
			defer framework.Stop(t.Context())

			config := fmt.Sprintf(`
				resource "podman_container" "test" {
					attach         = true
					container_host = "%s"
					image          = "example.com/library/test:v1.0.0"
					name           = "test"
				}
			`, framework.Url())

			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: providerFactories,
				CheckDestroy: func(_ *terraform.State) error {
					return apiServer.ContainerWalk(func(c *testutil.TestContainer) error {
						return fmt.Errorf("leftover container: %s", c.Json.Name)
					})
				},
				Steps: []resource.TestStep{
					{
						Config:      config,
						ExpectError: regexp.MustCompile(testCase.error),
					},
					{
						Config:  config,
						Destroy: true,
						Check:   resource.TestCheckResourceAttrSet("podman_container.test", "id"),
					},
				},
			})
		})
	}
}

func TestAccContainerResourceAttachDeferredStart(t *testing.T) {
	for _, start := range []struct {
		name   string
		config string
		value  string
	}{
		{name: "false", value: "false"},
		{
			name:   "unknown",
			config: `resource "terraform_data" "start" { input = false }`,
			value:  "terraform_data.start.output",
		},
	} {
		t.Run(start.name, func(t *testing.T) {
			apiServer := testutil.ApiServer{}
			framework, err := spawnFramework(t.Context(), &apiServer)
			assert.NilError(t, err)
			defer framework.Stop(t.Context())

			resource.Test(t, resource.TestCase{
				IsUnitTest:               true,
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
							%s
							resource "podman_container" "test" {
								attach            = true
								container_host    = "%s"
								image             = "example.com/library/test:v1.0.0"
								name              = "test"
								start_immediately = %s
							}
						`, start.config, framework.Url(), start.value),
						ExpectError: regexp.MustCompile("attach cannot be true when start_immediately is false"),
					},
				},
			})

			assert.NilError(t, apiServer.ContainerWalk(func(c *testutil.TestContainer) error {
				return fmt.Errorf("container created with invalid attach configuration: %s", c.Id)
			}))
		})
	}
}
