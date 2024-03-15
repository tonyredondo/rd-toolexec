package main

import (
	"fmt"
	"github.com/alexflint/go-filemutex"
	testAst "github.com/tonyredondo/rd-toolexec/internal/ast"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/processors"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/proxy"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
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
		return
	}

	pkgInj := processors.NewPackageInjectorWithRequired(testAst.ImportPath, GetSDKFolder(), "testing")

	if cmdT.Type() == proxy.CommandTypeCompile {
		for idx, val := range cmdT.Args() {
			if val == "-buildid" {
				testAst.SetBuildId(cmdT.Args()[idx+1])
				break
			}
		}

		compileCmd := cmdT.(*proxy.CompileCommand)
		for _, file := range compileCmd.GoFiles() {
			if strings.HasSuffix(file, "_test.go") {
				log.Printf("Adding %s", file)
				testAst.AppendTestFile(file)
			}
		}

		testAst.ProcessContainer()
		testContainers := testAst.GetTestContainer()
		replacementMap := map[string]string{}
		for _, container := range testContainers {
			for _, file := range container.Files {
				if file.DestinationFilePath != "" {
					log.Printf("Adding replacement: %s", file.DestinationFilePath)
					replacementMap[file.FilePath] = file.DestinationFilePath
				}
			}
		}

		if len(replacementMap) > 0 {
			log.Printf("Adding swapper for %v replacements", len(replacementMap))
			swapper := processors.NewGoFileSwapper(replacementMap)
			proxy.ProcessCommand(compileCmd, swapper.ProcessCompile)
			log.Println(compileCmd.Args())
		}

		proxy.ProcessCommand(compileCmd, pkgInj.ProcessCompile)
		proxy.MustRunCommand(compileCmd)
		return
	}

	if cmdT.Type() == proxy.CommandTypeLink {
		proxy.ProcessCommand(cmdT, pkgInj.ProcessLink)
		proxy.MustRunCommand(cmdT)
		return
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
