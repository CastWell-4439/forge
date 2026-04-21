// echo-plugin reads JSON from stdin and wraps it into a TaskOutput JSON on stdout.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -o echo.wasm ./cmd/echo-plugin
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type TaskOutput struct {
	Result string `json:"result"`
}

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v", err)
		os.Exit(1)
	}

	out := TaskOutput{Result: string(input)}
	enc, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v", err)
		os.Exit(1)
	}

	os.Stdout.Write(enc)
}
