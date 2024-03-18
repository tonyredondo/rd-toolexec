package gotest

import (
	"fmt"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/processors"
	"github.com/tonyredondo/rd-toolexec/internal/toolexec/proxy"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"golang.org/x/tools/go/ast/astutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	ImportName    string = "ddtesting"
	ImportPath    string = "github.com/DataDog/dd-sdk-go-testing/autoinstrument"
	importPathAst        = `"` + ImportPath + `"`
)

type GoTestProcessor struct {
	testingSdkSourcePath string
	packageInjector      processors.PackageInjector
}

type astSubTestData struct {
	TestName              string
	TestingTAttributeName string
	Call                  *ast.CallExpr
}

type astTestData struct {
	TestName                 string
	TestingTAttributeName    string
	AstDeclaration           *ast.FuncDecl
	SubTests                 []*astSubTestData
	Parent                   *astTestFileData
	IsMain                   bool
	IsTestMainGoFile         bool
	MRunCallInTestMainGoFile *ast.CallExpr
}

type astTestFileData struct {
	Tests                   []*astTestData
	FilePath                string
	FileSet                 *token.FileSet
	AstFile                 *ast.File
	Package                 string
	ContainsDDTestingImport bool
	Parent                  *astTestContainer
	TestMain                *astTestData
	IsTestMainGoFile        bool
	DestinationFilePath     string
}

type astTestContainer struct {
	Package string
	Files   []*astTestFileData
}

var (
	containers  []*astTestContainer
	buildId     string
	fileContent []string
)

func NewGoTestProcessor(sdkSourcePath string) GoTestProcessor {
	return GoTestProcessor{
		testingSdkSourcePath: sdkSourcePath,
		packageInjector:      processors.NewPackageInjectorWithRequired(ImportPath, sdkSourcePath, "testing"),
	}
}

func (p *GoTestProcessor) ProcessCompile(cmd *proxy.CompileCommand) {
	// Extract BuildId
	cmdArgs := cmd.Args()
	for idx, val := range cmdArgs {
		if val == "-buildid" {
			buildId = cmdArgs[idx+1]
			log.Printf("BuildId: %s\n", buildId)
			break
		}
	}

	// Process files from compile command
	for _, file := range cmd.GoFiles() {
		if strings.HasSuffix(file, "_test.go") ||
			strings.Contains(file, "_testmain.go") {
			// Let's process all _test.go files or the test binary main file
			log.Printf("Adding %s\n", file)
			testData, err := createTestData(file)
			if err == nil {
				var selectedContainer *astTestContainer
				for _, container := range containers {
					if container.Package == testData.Package {
						selectedContainer = container
						break
					}
				}
				if selectedContainer == nil {
					selectedContainer = new(astTestContainer)
					selectedContainer.Package = testData.Package
					containers = append(containers, selectedContainer)
				}
				testData.Parent = selectedContainer
				selectedContainer.Files = append(selectedContainer.Files, testData)
			}
		}
	}

	if len(containers) > 0 {
		// We have data to process.
		processContainer()
	}

	// Create replacement map from processed files
	replacementMap := map[string]string{}
	for _, container := range containers {
		for _, file := range container.Files {
			if file.DestinationFilePath != "" {
				log.Printf("Adding replacement: %s\n", file.DestinationFilePath)
				replacementMap[file.FilePath] = file.DestinationFilePath
			}
		}
	}

	// Add replacement processor
	if len(replacementMap) > 0 {
		log.Printf("Adding swapper for %v replacements", len(replacementMap))
		swapper := processors.NewGoFileSwapper(replacementMap)
		proxy.ProcessCommand(cmd, swapper.ProcessCompile)
		log.Println(cmd.Args())
	}

	// Add library injection processor
	proxy.ProcessCommand(cmd, p.packageInjector.ProcessCompile)
}

func (p *GoTestProcessor) ProcessLink(cmd *proxy.LinkCommand) {
	// Add library injection processor
	proxy.ProcessCommand(cmd, p.packageInjector.ProcessLink)
}

func createTestData(file string) (*astTestFileData, error) {
	fileSet := token.NewFileSet()
	testFileData := new(astTestFileData)
	testFileData.FilePath = file
	testFileData.FileSet = fileSet

	var testDataArray []*astTestData
	astFile, err := parser.ParseFile(fileSet, file, nil, parser.SkipObjectResolution)
	if err != nil {
		return testFileData, err
	} else {
		testFileData.AstFile = astFile
		testFileData.Package = astFile.Name.String()
		testFileData.ContainsDDTestingImport = false
		for _, v2 := range astFile.Imports {
			if v2.Name.String() == ImportName && v2.Path.Value == importPathAst {
				testFileData.ContainsDDTestingImport = true
				break
			}
		}

		ast.Inspect(astFile, func(n ast.Node) bool {
			if funcDecl, ok := n.(*ast.FuncDecl); ok {
				if strings.HasPrefix(funcDecl.Name.String(), "Test") {

					// Let's extract the parameter name for `testing.T`
					var tParam string
					isMain := false
					for _, pM := range funcDecl.Type.Params.List {
						for _, pMName := range pM.Names {
							switch pMType := pM.Type.(type) {
							case *ast.StarExpr:
								switch xType := pMType.X.(type) {
								case *ast.SelectorExpr:
									if xType.X.(*ast.Ident).String() == "testing" {
										xTypeSel := xType.Sel.String()
										if xTypeSel == "T" {
											tParam = pMName.Name
											break
										}
										if xTypeSel == "M" {
											tParam = pMName.Name
											isMain = true
											break
										}
									}
								}
							}
						}
					}

					testData := new(astTestData)
					testData.TestName = funcDecl.Name.Name
					testData.TestingTAttributeName = tParam
					testData.AstDeclaration = funcDecl
					testData.Parent = testFileData
					testData.IsMain = isMain

					if testData.TestName == "TestMain" {
						testFileData.TestMain = testData
					}

					ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
						switch t := node.(type) {
						case *ast.CallExpr:
							if fun, ok := t.Fun.(*ast.SelectorExpr); ok {
								if fun.Sel.Name == "Run" {
									if testData.TestingTAttributeName == fmt.Sprintf("%v", fun.X) {
										innerTest := new(astSubTestData)
										innerTest.TestName = fmt.Sprintf("%v.%v", fun.X, fun.Sel.String())
										innerTest.TestingTAttributeName = ""
										innerTest.Call = t
										testData.SubTests = append(testData.SubTests, innerTest)
									}
								}
							}
						}
						return true
					})

					testDataArray = append(testDataArray, testData)
				} else if strings.HasPrefix(funcDecl.Name.String(), "main") {
					ast.Inspect(funcDecl.Body, func(bNode ast.Node) bool {
						if callExpr, ok := bNode.(*ast.CallExpr); ok {
							if fun, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
								if fun.Sel.Name == "Run" && fmt.Sprintf("%v", fun.X) == "m" {
									testData := new(astTestData)
									testData.TestName = "_testmain.go"
									testData.TestingTAttributeName = "m"
									testData.AstDeclaration = funcDecl
									testData.Parent = testFileData
									testData.IsTestMainGoFile = true
									testData.MRunCallInTestMainGoFile = callExpr
									testFileData.IsTestMainGoFile = true
									testDataArray = append(testDataArray, testData)
									return false
								}
							}
						}
						return true
					})
					return false
				}
			}
			return true
		})
	}

	testFileData.Tests = testDataArray
	return testFileData, nil
}

func processContainer() {

	filePath := path.Join(os.TempDir(), fmt.Sprintf(".test_main_packages_%s", buildId))
	if bytes, err := os.ReadFile(filePath); err == nil {
		fileContent = strings.Split(string(bytes), "\n")
	}

	for _, container := range containers {
		if len(container.Files) > 0 {
			isDirty := false
			hasTestMainGoFile := false
			var testMainTestData *astTestData
			for _, file := range container.Files {
				isDirty = processFile(file) || isDirty
				hasTestMainGoFile = file.IsTestMainGoFile || hasTestMainGoFile
				if file.TestMain != nil {
					testMainTestData = file.TestMain
				}
			}

			if testMainTestData == nil && !hasTestMainGoFile {

				if !strings.HasSuffix(container.Package, "_test") {
					// This package doesn't have a TestMain func, let's check if this is a black box testing scenario before trying to create a TestMain func
					packageWithTest := fmt.Sprintf("%v_test", container.Package)
					if slices.ContainsFunc(containers, func(c *astTestContainer) bool { return c.Package == packageWithTest }) {
						// A _test package for this package name was found, we skip the injection on this package and left TestMain on the `_test` package
						continue
					}

					if slices.Contains(fileContent, packageWithTest) {
						// A _test package for this package name was found from a previous command execution
						continue
					}
				} else {
					// This is a test package let's check if we already processed the non _test package already before injecting the TestMain func
					packageWithoutTest := container.Package[0 : len(container.Package)-5]
					if slices.Contains(fileContent, packageWithoutTest) {
						// A non _test package for this package name was found from a previous command execution
						continue
					}
				}

				for _, packageFile := range container.Files {
					if !astutil.UsesImport(packageFile.AstFile, "testing") {
						continue
					}

					if !astutil.UsesImport(packageFile.AstFile, ImportPath) {
						astutil.AddNamedImport(packageFile.FileSet, packageFile.AstFile, ImportName, ImportPath)
					}
					packageFile.AstFile.Decls = append(packageFile.AstFile.Decls, getTestMainDeclarationSentence(ImportName, "m"))

					if packageFile.DestinationFilePath == "" {
						fileName := filepath.Base(packageFile.FilePath)
						fileNameExt := path.Ext(fileName)
						fileName = fmt.Sprintf("%v_*_%v", strings.TrimRight(fileName, fileNameExt), fileNameExt)
						if tmpFile, err := os.CreateTemp("", fileName); err == nil {
							packageFile.DestinationFilePath = tmpFile.Name()
							tmpFile.Close()
						}
					}

					f, err := os.Create(packageFile.DestinationFilePath)
					if err == nil {
						defer f.Close()
						err = printer.Fprint(f, packageFile.FileSet, packageFile.AstFile)
						if err != nil {
							fmt.Println(err)
							continue
						}

						fileContent = append(fileContent, fmt.Sprintf("%s\n", packageFile.Package))
						os.WriteFile(filePath, []byte(strings.Join(fileContent, "\n")), 0666)
						break
					}
				}
			}
		}
	}
}

func processFile(file *astTestFileData) bool {
	if !file.ContainsDDTestingImport && len(file.Tests) > 0 {
		isDirty := false
		for _, test := range file.Tests {
			if test.IsTestMainGoFile && test.MRunCallInTestMainGoFile != nil {
				newSubTestCall := getTestMainRunCallExpression(ImportName, "m")
				newSubTestCall.Args = append(newSubTestCall.Args, test.MRunCallInTestMainGoFile.Args...)
				test.MRunCallInTestMainGoFile.Fun = newSubTestCall.Fun
				test.MRunCallInTestMainGoFile.Args = newSubTestCall.Args
				isDirty = true
			}
			for _, subTest := range test.SubTests {
				var newSubTestCall *ast.CallExpr
				if test.IsMain {
					newSubTestCall = getTestMainRunCallExpression(ImportName, test.TestingTAttributeName)
				} else {
					newSubTestCall = getStartSubTestSentence(ImportName, test.TestingTAttributeName)
				}
				newSubTestCall.Args = append(newSubTestCall.Args, subTest.Call.Args...)
				subTest.Call.Fun = newSubTestCall.Fun
				subTest.Call.Args = newSubTestCall.Args
				isDirty = true
			}
		}

		if isDirty {
			if !astutil.UsesImport(file.AstFile, ImportPath) {
				astutil.AddNamedImport(file.FileSet, file.AstFile, ImportName, ImportPath)
			}

			if file.DestinationFilePath == "" {
				fileName := filepath.Base(file.FilePath)
				fileNameExt := path.Ext(fileName)
				fileName = fmt.Sprintf("%v_*_%v", strings.TrimRight(fileName, fileNameExt), fileNameExt)
				if tmpFile, err := os.CreateTemp("", fileName); err == nil {
					log.Printf("%s was modified.\n", file.FilePath)
					file.DestinationFilePath = tmpFile.Name()
					tmpFile.Close()
				}
			}

			f, err := os.Create(file.DestinationFilePath)
			if err == nil {
				defer f.Close()
				err = printer.Fprint(f, file.FileSet, file.AstFile)
				if err == nil {
					return true
				} else {
					fmt.Println(err)
				}
			}
		}
	}
	return false
}

func getStartSubTestSentence(currentImportName string, varName string) *ast.CallExpr {
	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   &ast.Ident{Name: currentImportName},
			Sel: &ast.Ident{Name: "Run"},
		},
		Args: []ast.Expr{
			&ast.Ident{Name: varName},
		},
	}
}

func getTestMainRunCallExpression(currentImportName string, varName string) *ast.CallExpr {
	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   &ast.Ident{Name: currentImportName},
			Sel: &ast.Ident{Name: "RunM"},
		},
		Args: []ast.Expr{
			&ast.Ident{Name: varName},
		},
	}
}

func getTestMainDeclarationSentence(currentImportName string, varName string) *ast.FuncDecl {
	return &ast.FuncDecl{
		Name: &ast.Ident{Name: "TestMain"},
		Type: &ast.FuncType{
			Params: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{{Name: varName}},
						Type: &ast.StarExpr{
							X: &ast.SelectorExpr{
								X:   &ast.Ident{Name: "testing"},
								Sel: &ast.Ident{Name: "M"},
							},
						},
					},
				},
			},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: currentImportName},
							Sel: &ast.Ident{Name: "RunTestMain"},
						},
						Args: []ast.Expr{
							&ast.Ident{Name: varName},
						},
					},
				},
			},
		},
	}
}
