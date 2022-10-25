package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"

	"github.com/spf13/cobra"
)

const addMountImage = "justincormack/addmount:latest"

var (
	cli *client.Client

	entrypointFlag []string
	cmdFlag        []string
)

var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Debug a container using a image",
	Long:  `A way to interactively inspect a container filesystem with the utilities you need.`,
	Example: `
debug-ctr debug --target=my-distroless	
debug-ctr debug --image=busybox:1.28 --target=my-distroless
debug-ctr debug --image=docker.io/alpine:latest --target=my-distroless --copy-to=my-distroless-copy 
debug-ctr debug --image=docker.io/alpine:latest --target=my-distroless --copy-to=my-distroless-copy --entrypoint="/.debugger/sleep" --cmd="365d"
`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		debugImage, _ := cmd.PersistentFlags().GetString("image")
		targetContainer, _ := cmd.PersistentFlags().GetString("target")
		copyContainerName, _ := cmd.PersistentFlags().GetString("copy-to")
		entryPointOverride := entrypointFlag
		cmdOverride := cmdFlag

		ctx := context.Background()

		// Check target container exists
		_, err := cli.ContainerInspect(ctx, targetContainer)
		if err != nil {
			return err
		}

		if err := pullImage(ctx, debugImage); err != nil {
			return err
		}

		debugContainer := targetContainer
		dockerExecCmd := ""
		if copyContainerName == "" {
			if err := addMountToTargetContainer(ctx, debugImage, targetContainer); err != nil {
				return err
			}
			dockerExecCmd = fmt.Sprintf("docker exec -it %s /bin/sh", debugContainer)
		} else {

			if err := createCopyContainer(ctx, debugImage, targetContainer, copyContainerName, entryPointOverride, cmdOverride); err != nil {
				return err
			}
			dockerExecCmd = fmt.Sprintf(`docker exec -it %s /.debugger/sh -c "PATH=\$PATH:/.debugger /.debugger/sh"`, copyContainerName)
		}

		log.Println("-------------------------------")
		log.Println("Debug your container:")
		log.Printf("$ %s", dockerExecCmd)
		log.Println("-------------------------------")

		switch runtime.GOOS {
		//TODO: windows
		//TODO: linux
		case "darwin":

			args := fmt.Sprintf(`
		reopen
        tell current window
          create tab with default profile
          tell current session
            write text "%s"
          end tell
        end tell
      end tell`, strings.ReplaceAll(strings.ReplaceAll(dockerExecCmd, `\`, `\\`), `"`, `\"`))

			err := exec.Command("/usr/bin/osascript", "-e", "tell application \"iTerm\"", "-e", args).Run()
			if err != nil {
				log.Fatal(err)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(debugCmd)

	debugCmd.PersistentFlags().String("image", "docker.io/library/busybox:latest", "(optional) The image to use for debugging purposes")
	debugCmd.PersistentFlags().String("target", "", "(required) The target container to debug")
	debugCmd.PersistentFlags().String("copy-to", "", "(optional) The name of the copy container")
	debugCmd.PersistentFlags().StringArrayVar(&entrypointFlag, "entrypoint", nil, "(optional) The entrypoint to run when starting the debug container (if --copy-to is specified)")
	debugCmd.PersistentFlags().StringArrayVar(&cmdFlag, "cmd", nil, "(optional) The command to run when starting the debug container (if --copy-to is specified)")

	_ = debugCmd.MarkPersistentFlagRequired("target")
}

func pullImage(ctx context.Context, image string) error {
	reader, err := cli.ImagePull(ctx, image, types.ImagePullOptions{
		Platform: "linux/" + runtime.GOARCH,
	})
	if err != nil {
		return err
	}
	_, err = io.Copy(os.Stdout, reader)
	return err
}

// addMountToTargetContainer mounts the tools from a running container (e.g. `busybox`) into the target container **without** having to restart it.
// The benefit of this approach is that you wouldn't lose the running state of the container and the tools are available in the target container.
func addMountToTargetContainer(ctx context.Context, debugImage, targetContainer string) error {
	// Run toolkit image
	toolkitContainerResp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:      debugImage,
		Entrypoint: []string{"/bin/sh", "-c", "tail -f /dev/null"}, // keep container running in the background
	}, nil, nil, nil, "")
	if err != nil {
		return err
	}
	if err := cli.ContainerStart(ctx, toolkitContainerResp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	// Add mount to the original container
	if err := pullImage(ctx, addMountImage); err != nil {
		return err
	}
	addMountContainerResp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: addMountImage,
		Cmd:   []string{toolkitContainerResp.ID, "/bin", targetContainer, "/bin"},
	}, &container.HostConfig{
		AutoRemove: true,
		Privileged: true,
		PidMode:    "host",
		Binds: []string{
			"/var/run/docker.sock:/var/run/docker.sock",
		},
	}, nil, nil, "")
	if err != nil {
		return err
	}
	if err := cli.ContainerStart(ctx, addMountContainerResp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}
	statusCh, errCh := cli.ContainerWait(ctx, addMountContainerResp.ID, container.WaitConditionRemoved)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}

	// Remove the toolkit container
	if err := cli.ContainerRemove(ctx, toolkitContainerResp.ID, types.ContainerRemoveOptions{
		Force: true,
	}); err != nil {
		return err
	}
	return nil
}

// createCopyContainer creates a new container (a "copy") that is used to debug.
// For example, you can't run docker exec to troubleshoot your container if your container image does not include a shell or if your application crashes on startup.
// In these situations you can use debug-ctr debug with "--copy-to" to create a copy of the container with configuration values changed to aid debugging.
func createCopyContainer(ctx context.Context, debugImage, targetContainer, copyContainerName string, entryPointOverride, cmdOverride []string) error {
	// Create one volume per container to debug to avoid overwriting binaries
	volumeName := strings.Replace(strings.Replace(debugImage, ":", "_", 1), "/", "_", -1)
	volume := fmt.Sprintf("debug-ctr-%s", volumeName)
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: debugImage,
	}, &container.HostConfig{
		AutoRemove: true,
		Binds: []string{
			volume + ":" + "/bin",
		},
	}, nil, nil, "")
	if err != nil {
		return err
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	// Create the "copy" container
	inspect, err := cli.ContainerInspect(ctx, targetContainer)
	if err != nil {
		return err
	}

	var containerEntrypoint = inspect.Config.Entrypoint
	if len(entryPointOverride) > 0 {
		x := strslice.StrSlice{}
		for _, y := range entryPointOverride {
			x = append(x, y)
		}
		containerEntrypoint = x
	}
	log.Printf("entrypoint: %+v", containerEntrypoint)

	var containerCmd = inspect.Config.Cmd
	if len(cmdOverride) > 0 {
		x := strslice.StrSlice{}
		for _, y := range cmdOverride {
			x = append(x, y)
		}
		containerCmd = x
	}
	log.Printf("containerCmd: %+v", containerCmd)

	copyContainerCreateResp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:      inspect.Image,
		User:       inspect.Config.User,
		Env:        inspect.Config.Env,
		Entrypoint: containerEntrypoint,
		Cmd:        containerCmd,
		WorkingDir: inspect.Config.WorkingDir,
		Labels:     inspect.Config.Labels,
	}, &container.HostConfig{
		Binds: []string{
			volume + ":" + "/.debugger",
		},
	}, nil, nil, copyContainerName)
	if err != nil {
		return err
	}

	log.Printf("Starting debug container %s", copyContainerCreateResp.ID)
	if err := cli.ContainerStart(ctx, copyContainerCreateResp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}
	return nil
}
