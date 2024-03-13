package ast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"golang.org/x/tools/go/ast/astutil"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// go get github.com/DataDog/dd-sdk-go-testing@a3bdb65a82031481e2edfcbf819261560f3393f2

const (
	importName    string = "ddtesting"
	importPath    string = "github.com/DataDog/dd-sdk-go-testing/autoinstrument"
	importPathAst        = `"` + importPath + `"`
)

type astTestContainer struct {
	Package string
	Files   []*astTestFileData
}

var containers []*astTestContainer
var buildId string
var basePath string
var fileContent []string

type astTestFileData struct {
	Tests                   []*astTestData
	FilePath                string
	FileSet                 *token.FileSet
	AstFile                 *ast.File
	Package                 string
	ContainsDDTestingImport bool
	Parent                  *astTestContainer
	TestMain                *astTestData
}

type astTestData struct {
	TestName              string
	TestingTAttributeName string
	StartLine             int
	EndLine               int
	AstDeclaration        *ast.FuncDecl
	SubTests              []*astSubTestData
	Parent                *astTestFileData
	IsMain                bool
}

type astSubTestData struct {
	TestName              string
	TestingTAttributeName string
	Line                  int
	Call                  *ast.CallExpr
	SubTests              []*astSubTestData
}

func GetTestContainer() []*astTestContainer {
	return containers
}

func ResetTestContainer() {
	containers = make([]*astTestContainer, 0)
}

func SetBuildId(id string) {
	buildId = id
}

func AppendTestFile(file string) {
	basePath = filepath.Dir(file)
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
			if v2.Name.String() == importName && v2.Path.Value == importPathAst {
				testFileData.ContainsDDTestingImport = true
				break
			}
		}

		ast.Inspect(astFile, func(n ast.Node) bool {
			funcDecl, ok := n.(*ast.FuncDecl)
			if ok {
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
					testData.StartLine = fileSet.Position(funcDecl.Pos()).Line
					testData.EndLine = fileSet.Position(funcDecl.End()).Line
					testData.AstDeclaration = funcDecl
					testData.Parent = testFileData
					testData.IsMain = isMain

					if testData.TestName == "TestMain" {
						testFileData.TestMain = testData
					}

					innerData, err := createBodyTestData(testData, funcDecl.Body)
					if err == nil {
						testData.SubTests = innerData
					}

					testDataArray = append(testDataArray, testData)
				}
			}
			return true
		})
	}

	testFileData.Tests = testDataArray
	return testFileData, nil
}

func createBodyTestData(test *astTestData, body *ast.BlockStmt) ([]*astSubTestData, error) {
	visitor := new(innerTestRunVisitor)
	visitor.test = test
	ast.Walk(visitor, body)
	return visitor.subTests, visitor.err
}

type innerTestRunVisitor struct {
	test     *astTestData
	subTests []*astSubTestData
	err      error
}

func (v *innerTestRunVisitor) Visit(node ast.Node) (w ast.Visitor) {
	switch t := node.(type) {
	case *ast.CallExpr:
		if fun, ok := t.Fun.(*ast.SelectorExpr); ok {
			if fun.Sel.Name == "Run" {
				if v.test.TestingTAttributeName == fmt.Sprintf("%v", fun.X) {
					innerTest := new(astSubTestData)
					innerTest.TestName = fmt.Sprintf("%v.%v", fun.X, fun.Sel.String())
					innerTest.TestingTAttributeName = ""
					innerTest.Line = v.test.Parent.FileSet.Position(fun.Pos()).Line
					innerTest.Call = t
					v.subTests = append(v.subTests, innerTest)
				}
			}
		}
	}

	return v
}

func ProcessContainer() {

	//filePath := path.Join(os.TempDir(), fmt.Sprintf(".test_main_packages_%s", buildId))
	filePath := path.Join(basePath, ".test_main_packages")
	if bytes, err := os.ReadFile(filePath); err == nil {
		fileContent = strings.Split(string(bytes), "\n")
	}

	for _, container := range containers {
		if len(container.Files) > 0 {
			isDirty := false
			var testMainTestData *astTestData
			for _, file := range container.Files {
				isDirty = processFile(file) || isDirty
				if file.TestMain != nil {
					testMainTestData = file.TestMain
				}
			}

			if testMainTestData == nil {
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

					if !astutil.UsesImport(packageFile.AstFile, "os") {
						astutil.AddImport(packageFile.FileSet, packageFile.AstFile, "os")
					}
					if !astutil.UsesImport(packageFile.AstFile, importPath) {
						astutil.AddNamedImport(packageFile.FileSet, packageFile.AstFile, importName, importPath)
					}
					packageFile.AstFile.Decls = append(packageFile.AstFile.Decls, getTestMainDeclarationSentence(importName, "m"))

					f, err := os.Create(packageFile.FilePath)
					if err == nil {
						defer f.Close()
						err = printer.Fprint(f, packageFile.FileSet, packageFile.AstFile)
						if err != nil {
							fmt.Println(err)
							continue
						}

						fmt.Println("Writing package info")
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
			for _, subTest := range test.SubTests {
				var newSubTestCall *ast.CallExpr
				if test.IsMain {
					newSubTestCall = getTestMainRunCallExpression(importName, test.TestingTAttributeName)
				} else {
					newSubTestCall = getStartSubTestSentence(importName, test.TestingTAttributeName)
				}
				newSubTestCall.Args = append(newSubTestCall.Args, subTest.Call.Args...)
				subTest.Call.Fun = newSubTestCall.Fun
				subTest.Call.Args = newSubTestCall.Args
				isDirty = true
			}
		}

		if isDirty {
			if !astutil.UsesImport(file.AstFile, importPath) {
				astutil.AddNamedImport(file.FileSet, file.AstFile, importName, importPath)
			}

			f, err := os.Create(file.FilePath)
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
							X:   &ast.Ident{Name: "os"},
							Sel: &ast.Ident{Name: "Exit"},
						},
						Args: []ast.Expr{
							getTestMainRunCallExpression(currentImportName, varName),
						},
					},
				},
			},
		},
	}
}
