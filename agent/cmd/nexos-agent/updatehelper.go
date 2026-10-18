package main

import (
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// runUpdateHelper is executed in a short-lived helper container started by
// the agent. It recreates one compose service with the already pulled image
// and exits; the helper is not affected because it is not part of the
// compose project.
func runUpdateHelper(args []string) error {
	fs := flag.NewFlagSet("update-helper", flag.ContinueOnError)
	project := fs.String("project", "", "compose project name")
	workdir := fs.String("workdir", "", "compose project directory")
	service := fs.String("service", "", "service to recreate")
	var files multiFlag
	fs.Var(&files, "file", "compose file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Let the agent finish answering the hub before it may be replaced.
	time.Sleep(3 * time.Second)
	cmdArgs := []string{"compose", "--ansi", "never", "-p", *project, "--project-directory", *workdir}
	for _, f := range files {
		cmdArgs = append(cmdArgs, "-f", f)
	}
	cmdArgs = append(cmdArgs, "up", "-d", "--no-deps", "--force-recreate", "--pull", "never", *service)
	slog.Info("update helper", "args", cmdArgs)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

type multiFlag []string

func (m *multiFlag) String() string     { return "" }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
