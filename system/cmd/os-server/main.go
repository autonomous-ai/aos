package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	ccgatewayd "go.autonomous.ai/os/runtimes/claudecode/gatewayd"
	"go.autonomous.ai/os/runtimes/codex/gatewayd"
	ocgatewayd "go.autonomous.ai/os/runtimes/opencode/gatewayd"
	"go.autonomous.ai/os/system/lib/hal"
	"go.autonomous.ai/os/system/lib/logger"
	"go.autonomous.ai/os/system/lib/netfallback"
	"go.autonomous.ai/os/system/lib/safego"
	"go.autonomous.ai/os/system/lib/syspath"
	"go.autonomous.ai/os/system/lib/versioncache"
	"go.autonomous.ai/os/system/server"
	"go.autonomous.ai/os/system/server/config"
)

func main() {
	// Subcommands run the backend WS bridges shipped inside this binary.
	if len(os.Args) > 1 && os.Args[1] == "codex-gatewayd" {
		os.Exit(gatewayd.Main())
	}
	if len(os.Args) > 1 && os.Args[1] == "claudecode-gatewayd" {
		os.Exit(ccgatewayd.Main())
	}
	if len(os.Args) > 1 && os.Args[1] == "opencode-gatewayd" {
		os.Exit(ocgatewayd.Main())
	}
	if len(os.Args) > 1 && os.Args[1] == "claude-sessions" {
		os.Exit(ccMain(os.Args[2:]))
	}

	var showVersion, waitHAL bool
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&waitHAL, "wait-hal-ready", false, "wait up to 60 seconds for HAL and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(config.OSVersion)
		return
	}

	if waitHAL {
		os.Exit(waitHALMain())
	}

	// Load before logger init so GELF_* vars are visible; missing file is fine.
	_ = godotenv.Load("/opt/hal/.env")

	cleanup := logger.Init(syspath.LogFile())
	defer cleanup()
	// Keep what cannot ship yet: a first setup runs with no key and no internet,
	// and its records are the ones that explain a failed registration.
	logger.EnableGELFSpool(syspath.GELFSpoolDir(), "os-server")

	// CLI version probes wait for HAL readiness so they don't compete for storage at boot.
	probeCtx, cancelProbes := context.WithCancel(context.Background())
	defer cancelProbes()
	versioncache.ConfigureStartup(probeCtx, func(ctx context.Context) bool {
		requestCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := hal.GetHealthContext(requestCtx)
		return err == nil
	}, time.Minute)

	// Fallback time and DNS servers for the region the cloud reports; a no-op
	// once applied.
	safego.Go("netfallback", func() { netfallback.NewApplier(configRegion).Run(probeCtx) })

	srv, err := server.InitializeServer()
	if err != nil {
		log.Fatal("initialize server: ", err)
	}
	if err := srv.Serve(cancelProbes); err != nil {
		log.Fatal("http server: ", err)
	}
}

// configRegion returns the region the cloud last reported in the ping reply,
// as saved in config.json ("" for none), and whether the device is set up.
// It is a plain read of the file, with no config.Load: it runs every few
// minutes and must never trigger config migrations or a save.
func configRegion() (string, bool) {
	data, err := os.ReadFile(config.Path())
	if err != nil {
		return "", false
	}
	var c struct {
		Region         string `json:"region"`
		SetUpCompleted bool   `json:"set_up_completed"`
	}
	if json.Unmarshal(data, &c) != nil {
		return "", false
	}
	return c.Region, c.SetUpCompleted
}
