# debug-ctr

A commandline tool for interactive troubleshooting when a container has crashed or a container image doesn't include debugging utilities, such as distroless images. Heavily inspired by `kubectl debug`, but for containers instead of Pods.

## Option 1: Debugging adding a mount

This approach uses [justincormack/addmount](https://github.com/justincormack/addmount) to mount the tools from a running container (e.g. `busybox`) into a target container **without** having to restart it.
The benefit of this approach is that you wouldn't lose the running state of the container and the tools are available in the target container.

For example, you can run the following container from a distroless image that doesn't have a shell:

```shell
docker run -d --rm \
  --name my-distroless gcr.io/distroless/nodejs \
  -e 'setTimeout(() => console.log("Done"), 99999999)'
```

If you try to access the container, it'll fail because it doesn't contain a shell:

```shell
docker exec -it my-distroless /bin/sh
OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory: unknown
```

You can bring the tools from `busybox:1.28` that are available in `/bin` into the target container (**without** having to restart it) by simply running:

```shell
debug-ctr debug --image=busybox:1.28 --target=my-distroless

...
2022/10/25 09:32:40 -------------------------------
2022/10/25 09:32:40 Debug your container:
2022/10/25 09:32:40 $ docker exec -it my-distroless /bin/sh
2022/10/25 09:32:40 -------------------------------
```

## Option 2: Debugging using a "copy" of the container

Sometimes a container configuration options make it difficult to troubleshoot in certain situations. For example, you can't run `docker exec` to troubleshoot your container if your container image does not include a shell or if your application crashes on startup. In these situations you can use `debug-ctr debug` to create a "copy" of the container with configuration values changed to aid debugging.

### How does it work?

`debug-ctr debug` uses the `--copy-to` flag to run a new container (a "copy" a.k.a the debugger container) that can be useful when your application is running but not behaving as you expect, and you'd like to add additional troubleshooting utilities to the container. This new container is simply a "copy" of the container you want to debug which now includes the utilities tools that you need to debug it.

The tools are first downloaded into a Docker volume from the image you specify with the `--image` flag from the `/bin` directory. When the debugger container is created, the volume is mounted at `/.debugger` and thus the tools in `/bin` from the image are available in the debugger container filesystem (e.g. `ls` will be available at `/.debugger/ls`) and added to the `PATH` automatically for you.

You can bring the `sh` tool from `busybox:1.28` and simply run the following command to **create a new debugger container** and use the `docker exec` command suggested in the output to access it:

```shell
debug-ctr debug --image=busybox:1.28 --target=my-distroless --copy-to=my-distroless-copy

...
2022/10/22 20:09:26 Starting debug container my-distroless-copy
2022/10/22 20:09:26 -------------------------------
2022/10/22 20:09:26 Debug your container:
2022/10/22 20:09:26 $ docker exec -it my-distroless-copy /.debugger/sh -c "PATH=\$PATH:/.debugger /.debugger/sh"
2022/10/22 20:09:26 -------------------------------
```

Note that with this approach the `docker exec` command from the output is used to **exec into the debugger container, not into the original one**.

### Changing its entrypoint and/or command

Sometimes it's useful to change the entrypoint and/or command for a container, for example to add a debugging flag or because the application is crashing.

To simulate a crashing application, use docker run to create a container that immediately exits:

```shell
docker run --name crashing-container busybox:1.28 /bin/sh -c "false"
```

You can use `debug-ctr debug` with `--entrypoint` and/or `--cmd` to create a copy of this container with the command changed to an interactive shell:

```shell
debug-ctr debug --image=docker.io/alpine:latest --target=crashing-container --copy-to=crashing-container-copy --entrypoint="/.debugger/sleep" --cmd="365d"
```

Now you have an interactive shell that you can use to perform tasks like checking filesystem paths or running a container command manually.

## Acknowledgements

- https://iximiuz.com/en/posts/docker-debug-slim-containers/
