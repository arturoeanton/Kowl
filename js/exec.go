package js

import (
	"bytes"
	"fmt"
	"os/exec"
)

func KExec(name string, arg ...string) (string, string, int) {
	cmd := exec.Command(name, arg ...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Sprintf("kExec failed with %s", err), -6

	}
	return string(stdout.Bytes()), string(stderr.Bytes()), 0
}