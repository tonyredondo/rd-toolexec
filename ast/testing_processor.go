package ast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"golang.org/x/tools/go/ast/astutil"
	"os"
	"strings"
)

// go get github.com/DataDog/dd-sdk-go-testing@a3bdb65a82031481e2edfcbf819261560f3393f2

const (
	importName             string = "ddtesting"
	importPath             string = "github.com/DataDog/dd-sdk-go-testing/autoinstrument"
	importPathAst                 = `"` + importPath + `"`
	finishFuncVariableName string = "testFinishFunc"
)

type astTestContainer struct {
	Package string
	Files   []*astTestFileData
}

var containers []*astTestContainer

type astTestFileData struct {
	Tests                   []*astTestData
	FilePath                string
	FileSet                 *token.FileSet
	AstFile                 *ast.File
	Package                 string
	ContainsDDTestingImport bool
	Parent                  *astTestContainer
	HasTestMain             bool
}

type astTestData struct {
	TestName              string
	TestingTAttributeName string
	StartLine             int
	EndLine               int
	AstDeclaration        *ast.FuncDecl
	SubTests              []*astSubTestData
	Parent                *astTestFileData
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

func AppendTestFile(file string) {
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
					for _, pM := range funcDecl.Type.Params.List {
						for _, pMName := range pM.Names {
							switch pMType := pM.Type.(type) {
							case *ast.StarExpr:
								switch xType := pMType.X.(type) {
								case *ast.SelectorExpr:
									if xType.Sel.String() == "T" && xType.X.(*ast.Ident).String() == "testing" {
										tParam = pMName.Name
										break
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

					testFileData.HasTestMain = testFileData.HasTestMain || testData.TestName == "TestMain"

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
	for _, container := range containers {
		if len(container.Files) > 0 {
			isDirty := false
			hasTestMain := false
			for _, file := range container.Files {
				isDirty = processFile(file) || isDirty
				hasTestMain = hasTestMain || file.HasTestMain
			}

			if !hasTestMain {
				packageFile := container.Files[0]
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
					}
				}
			} else {
				fmt.Println("TODO: Modify current TestMain method")
			}
		}
	}
}

func processFile(file *astTestFileData) bool {
	if !file.ContainsDDTestingImport && len(file.Tests) > 0 {
		isDirty := false
		for _, test := range file.Tests {
			for _, subTest := range test.SubTests {
				newSubTestCall := getStartSubTestSentence(importName, test.TestingTAttributeName)
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
		/*
			astutil.AddNamedImport(file.FileSet, file.AstFile, importName, importPath)

			for _, test := range file.Tests {
				test.AstDeclaration.Body.List = append([]ast.Stmt{
					getStartTestSentence(importName, test.TestingTAttributeName),
					getDeferFinishSentence(),
				}, test.AstDeclaration.Body.List...)
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
		*/
	}
	return false
}

/*
func getStartTestSentence(currentImportName string, varName string) *ast.AssignStmt {
	return &ast.AssignStmt{
		Lhs: []ast.Expr{
			&ast.Ident{Name: "_"},
			&ast.Ident{Name: finishFuncVariableName},
		},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{
			&ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   &ast.Ident{Name: currentImportName},
					Sel: &ast.Ident{Name: "StartTest"},
				},
				Args: []ast.Expr{
					&ast.Ident{Name: varName},
				},
			},
		},
	}
}

func getDeferFinishSentence() *ast.DeferStmt {
	return &ast.DeferStmt{
		Call: &ast.CallExpr{
			Fun: &ast.Ident{
				Name: finishFuncVariableName,
			},
		},
	}
}
*/

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
							&ast.CallExpr{
								Fun: &ast.SelectorExpr{
									X:   &ast.Ident{Name: currentImportName},
									Sel: &ast.Ident{Name: "RunM"},
								},
								Args: []ast.Expr{
									&ast.Ident{Name: varName},
								},
							},
						},
					},
				},
			},
		},
	}
}
