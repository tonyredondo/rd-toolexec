package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	testAst "rd-toolexec/ast"
	"regexp"
)

func main() {
	tool, args := os.Args[1], os.Args[2:]
	toolName := filepath.Base(tool)
	if len(args) > 0 && args[0] == "-V=full" {
		// We can't alter the version output.
	} else {
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
					fmt.Printf("\tFile: %v | ContainsDDTestingImport: %v | HasTestMain: %v\n", file.FilePath, file.ContainsDDTestingImport, file.HasTestMain)
					for _, test := range file.Tests {
						fmt.Printf("\t\t%v (%v-%v) %v\n", test.TestName, test.StartLine, test.EndLine, test.TestingTAttributeName)
						for _, subTest := range test.SubTests {
							fmt.Printf("\t\t\t%v (%v) | %v\n", subTest.TestName, subTest.Line, subTest.Call)
						}
					}
				}
			}

			testAst.ProcessContainer()
		}

		// fmt.Println(toolName, args)
	}

	// Simply run the tool.
	cmd := exec.Command(tool, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
