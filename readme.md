# supervisor

This is a work-in-progress.

This program is a very naive supervision process that is intended to be run as
PID 1 in a container running within a Kubernetes Pod. The supervision process
launches a child process; this child process is the "true" program running in
the container.

The supervisor waits for source file changes in the container. When it
detects a change (notified with a change to the modification time of a special
file), the supervision process triggers a rebuild, kills the old process, and
runs the new program.

The intention is to tighten the development cycle, since this approach avoids
having to push an image or schedule a new deployment for each code change.

TODO: Add usage examples.

TODO: Describe assumptions: you assume a certain structure to the pod name, you are currently using Go, etc.
