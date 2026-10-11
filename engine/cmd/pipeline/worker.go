package main

import (
	"context"
	"fmt"
	"os"

	"github.com/vimalyad/oss-pipeline/engine/internal/identity"
	"github.com/vimalyad/oss-pipeline/engine/internal/llm"
)

func workerCheckCmd() int {
	brain := llm.New(identity.SandboxEnv())
	brain.Log = func(s string) { fmt.Println(s) }
	if err := brain.Check(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "worker check:", err)
		return 1
	}
	return 0
}
