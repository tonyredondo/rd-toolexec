package main

import (
	"fmt"
	"github.com/alexflint/go-filemutex"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/processors/gotest"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/proxy"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
)

var root string

func main() {
	if len(os.Args) == 1 {
		GetSDKFolder()
		return
	}

	log.SetOutput(io.Discard)
	cmdT := proxy.MustParseCommand(os.Args[1:])

	if cmdT.Type() == proxy.CommandTypeOther {
		proxy.MustRunCommand(cmdT)
	} else {
		goTestProcessor := gotest.NewGoTestProcessor(GetSDKFolder())
		if cmdT.Type() == proxy.CommandTypeCompile {
			compileCmd := cmdT.(*proxy.CompileCommand)
			proxy.ProcessCommand(compileCmd, goTestProcessor.ProcessCompile)
		} else if cmdT.Type() == proxy.CommandTypeLink {
			linkCmd := cmdT.(*proxy.LinkCommand)
			proxy.ProcessCommand(linkCmd, goTestProcessor.ProcessLink)
		}

		proxy.MustRunCommand(cmdT)
	}
}

func GetSDKFolder() string {
	finalSdk := ""
	noArgument := len(os.Args) == 1
	m, lockError := filemutex.New("/tmp/dd-sdk-go-testing.lock")
	if lockError == nil {
		m.Lock()
	}

	sdkPaths := []string{
		path.Join(root, "external", "dd-sdk-go-testing"),
		path.Join(os.TempDir(), "dd-sdk-go-testing"),
	}

	for _, sdkPath := range sdkPaths {
		autoInstrumentPath := path.Join(sdkPath, "autoinstrument")
		if _, err := os.Stat(autoInstrumentPath); err == nil {
			// SDK found
			finalSdk = autoInstrumentPath
			if noArgument {
				fmt.Printf("SDK found at: %s\n", finalSdk)
			}
			break
		}
	}
	if finalSdk == "" {
		tmpPath := sdkPaths[1]
		if noArgument {
			fmt.Printf("Downloading SDK to: %s\n", tmpPath)
		}
		cmdGitClone := exec.Command("git", "clone", "https://github.com/DataDog/dd-sdk-go-testing.git")
		cmdGitClone.Dir = os.TempDir()
		cmdGitClone.CombinedOutput()

		cmdGitCheckout := exec.Command("git", "checkout", "tony/rd-autoinstrument")
		cmdGitCheckout.Dir = tmpPath
		cmdGitCheckout.CombinedOutput()
		finalSdk = path.Clean(path.Join(tmpPath, "autoinstrument"))
	}

	if lockError == nil {
		m.Unlock()
	}
	return finalSdk
}

func init() {
	_, file, _, _ := runtime.Caller(0)
	root = path.Dir(file)
}
