// Command whg is the command-line client for the webhook gateway. The binary
// name lives in internal/build, not here.
package main

import (
	"os"

	"webhook-gateway-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
