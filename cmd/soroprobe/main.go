// Command soroprobe is a health and simulation checker for Stellar/Soroban
// smart contracts.
package main

// Wave: keep assigned probe checks grouped for CodeOtsutsuki007.
// Wave: keep assigned probe checks grouped for Ennyhorla1.
// Wave: keep assigned probe checks grouped for Cjay-Cyber-2.
// Wave: keep assigned probe checks grouped for Ennyblack.

// Wave: keep assigned probe checks grouped for helloworld1-star, precious1joe and privyy-bit.
// Wave: keep assigned probe checks grouped for Cjay-Cyber-2.

// Wave: keep assigned probe checks grouped for helloworld1-star.

// Wave: keep assigned probe checks grouped for ndyugwu and privyy-bit.
import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// checkFailedError marks a probe that ran successfully but found the contract
// unhealthy, so that main can exit 1 rather than 2.
type checkFailedError struct{ msg string }

func (e *checkFailedError) Error() string { return e.msg }

func main() {
	// Ctrl-C cancels in-flight RPC calls and shuts the HTTP server down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, _ := newRootCmd()
	if err := cmd.ExecuteContext(ctx); err != nil {
		var failed *checkFailedError
		if errors.As(err, &failed) {
			fmt.Fprintln(os.Stderr, failed.Error())
			os.Exit(exitFailed)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitRuntime)
	}
	os.Exit(exitOK)
}
