package cmd

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"io"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var (
	cli *client.Client

	entrypointFlag []string
	cmdFlag        []string
)

// debugCmd represents the debug command
var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Debug a container using a image",
	Long:  `A way to interactively inspect a container filesystem with the utilities you need.`,
	Example: `debug-ctr debug --image=busybox:1.28 --target=my-distroless
debug-ctr debug --image=docker.io/alpine:latest --target=my-distroless --entrypoint="/.debugger/sleep" --cmd="365d"
`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		debugImage, _ := cmd.PersistentFlags().GetString("image")
		targetContainer, _ := cmd.PersistentFlags().GetString("target")
		entryPointOverride := entrypointFlag
		cmdOverride := cmdFlag

		ctx := context.Background()

		// Get the bin folder of the image fs into a volume
		reader, err := cli.ImagePull(ctx, debugImage, types.ImagePullOptions{
			Platform: "linux/" + runtime.GOARCH,
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(os.Stdout, reader)
		if err != nil {
			return err
		}

		// Create one volume per container to debug to avoid overwriting binaries
		volumeName := strings.Replace(strings.Replace(debugImage, ":", "_", 1), "/", "_", 1)
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

		// Create debug container

		inspect, err := cli.ContainerInspect(ctx, targetContainer)
		if err != nil {
			return err
		}

		// For example, you can't run docker exec to troubleshoot your container if your container image does not include a shell or if your application crashes on startup.
		// In these situations you can use debug-ctr debug to create a copy of the container with configuration values changed to aid debugging.
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

		targetContainerCreate, err := cli.ContainerCreate(ctx, &container.Config{
			Image:      inspect.Image,
			User:       inspect.Config.User,
			Env:        inspect.Config.Env,
			Entrypoint: containerEntrypoint,
			Cmd:        containerCmd,
			WorkingDir: inspect.Config.WorkingDir,
			Labels:     inspect.Config.Labels,
		}, &container.HostConfig{
			Binds: []string{
				//TODO: provide support for nixery images
				volume + ":" + "/.debugger",
			},
		}, nil, nil, "")
		if err != nil {
			return err
		}

		log.Printf("Starting debug container %s", targetContainerCreate.ID)
		if err := cli.ContainerStart(ctx, targetContainerCreate.ID, types.ContainerStartOptions{}); err != nil {
			return err
		}

		log.Println("-------------------------------")
		log.Println("Debug your container:")
		log.Printf(`$ docker exec -it %s /.debugger/sh -c "PATH=\$PATH:/.debugger /.debugger/sh"`, targetContainerCreate.ID)
		log.Println("-------------------------------")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(debugCmd)

	debugCmd.PersistentFlags().String("image", "", "(required) The image to use for debugging purposes")
	debugCmd.PersistentFlags().String("target", "", "(required) The container to debug")
	debugCmd.PersistentFlags().StringArrayVar(&entrypointFlag, "entrypoint", nil, "(optional) The entrypoint to run when starting the debug container")
	debugCmd.PersistentFlags().StringArrayVar(&cmdFlag, "cmd", nil, "(optional) The command to run when starting the debug container")

	_ = debugCmd.MarkPersistentFlagRequired("image")
	_ = debugCmd.MarkPersistentFlagRequired("target")
}
