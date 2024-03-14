package main

import (
	"io"
	"log"
	"os"
	"path"
	testAst "rd-toolexec/internal/ast"
	"rd-toolexec/internal/toolexec/processors"
	"rd-toolexec/internal/toolexec/proxy"
	"runtime"
	"strings"
)

var root string

func main() {
	log.SetOutput(io.Discard)
	cmdT := proxy.MustParseCommand(os.Args[1:])

	if cmdT.Type() == proxy.CommandTypeOther {
		proxy.MustRunCommand(cmdT)
		return
	}

	pkgInj := processors.NewPackageInjectorWithRequired(
		testAst.ImportPath,
		path.Join(root, "external", "dd-sdk-go-testing", "autoinstrument"),
		"testing")

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

	/*
		tool, args := os.Args[1], os.Args[2:]
		toolName := filepath.Base(tool)
		if len(args) > 0 && args[0] == "-V=full" {
			// We can't alter the version output.
		} else {
			// proxy.ProcessCommand(cmdT, )

			proxy.RunCommand(cmdT)
			_ = cmdT

			if toolName == "compile" {
				var re = regexp.MustCompile(`(?m)^.*_test\.go$`)
				for _, v := range args {
					if re.MatchString(v) {
						nv, err := filepath.Abs(v)
						if err == nil {
							testAst.AppendTestFile(nv)
						} else {
							testAst.AppendTestFile(v)
						}
					}
				}

					containers := testAst.GetTestContainer()
					for _, container := range containers {
						fmt.Printf("Package: %v\n", container.Package)
						for _, file := range container.Files {
							fmt.Printf("\tFile: %v | ContainsDDTestingImport: %v | HasTestMain: %v\n", file.FilePath, file.ContainsDDTestingImport, file.TestMain != nil)
							for _, test := range file.Tests {
								fmt.Printf("\t\t%v (%v-%v) %v\n", test.TestName, test.StartLine, test.EndLine, test.TestingTAttributeName)
								for _, subTest := range test.SubTests {
									fmt.Printf("\t\t\t%v (%v) | %v\n", subTest.TestName, subTest.Line, subTest.Call)
								}
							}
						}
					}

				for idx, val := range args {
					if val == "-buildid" {
						testAst.SetBuildId(args[idx+1])
						break
					}
				}
				testAst.ProcessContainer()
			}
		}

		// Simply run the tool.
		cmd := exec.Command(tool, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	*/
}

func init() {
	_, file, _, _ := runtime.Caller(0)
	root = path.Dir(file)
}
