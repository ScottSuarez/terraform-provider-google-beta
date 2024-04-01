package temp

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
)

type resourceSourceMetadata struct {
	createSourceFile string
	readSourceFile   string
	readFuncName     string
	createFuncName   string
	schema           map[string]*schema.Schema
}

var functionDeclarations = make(map[string]*ast.FuncDecl)
var functionCallOrder [][]string
var packageVars = make(map[string]*ast.GenDecl)
var accessedVars = make(map[string]bool)

func createMasterFile(resourceType, testName string, resourceMetadata map[string]resourceSourceMetadata) {
	resourceMD, ok := resourceMetadata[resourceType]
	if !ok {
		// resource not supported
		return
	}
	fset := token.NewFileSet()

	dir := filepath.Dir(resourceMD.createSourceFile)
	functionNames := []string{resourceMD.createFuncName, resourceMD.readFuncName}

	// Parse all files in the directory
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		panic(err)
	}

	// Since there's only one package in the directory, we directly access it.
	pkg := pkgs[filepath.Base(dir)]
	collectPackageVars(pkg)
	for _, funcName := range functionNames {
		collectFunction(pkg, funcName, 0)
	}

	buf := &bytes.Buffer{}

	for varName, varDecl := range packageVars {
		// Only print the variables that are accessed by the functions
		if _, accessed := accessedVars[varName]; accessed {
			printer.Fprint(buf, fset, varDecl)
			buf.WriteString("\n")
		}
	}

	printedFunctions := make(map[string]bool)
	for _, funcs := range functionCallOrder {
		for _, funcName := range funcs {
			if funcDecl, exists := functionDeclarations[funcName]; exists {
				printer.Fprint(buf, fset, funcDecl)
				buf.WriteString("\n\n")
				printedFunctions[funcName] = true
			}
		}
	}

	// print any un printed functions
	for _, funcName := range functionNames {
		if funcDecl, exists := functionDeclarations[funcName]; exists {
			printer.Fprint(buf, fset, funcDecl)
			buf.WriteString("\n\n")
			printedFunctions[funcName] = true
		}
	}

	for funcName, funcDecl := range functionDeclarations {
		if !printedFunctions[funcName] {
			printer.Fprint(buf, fset, funcDecl)
			buf.WriteString("\n\n")
			printedFunctions[funcName] = true
		}
	}

	// logTestdata(testName, "master_file.go", buf.String(), resourceType)
}

func collectFunction(pkg *ast.Package, funcName string, callLevel int) {
	if _, exists := functionDeclarations[funcName]; exists {
		return
	}

	// Ensure that the functionCallOrder is large enough to hold this call level.
	// If it's not, grow it to the necessary size.
	for len(functionCallOrder) <= callLevel {
		functionCallOrder = append(functionCallOrder, nil)
	}

	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Name.Name == funcName {
					functionDeclarations[fn.Name.Name] = fn
					functionCallOrder[callLevel] = append(functionCallOrder[callLevel], fn.Name.Name)
					for _, call := range collectCalledFunctions(pkg, fn.Body) {
						collectFunction(pkg, call, callLevel+1)
					}
				}
			}
			return true
		})
	}
}

func inspectCallArg(pkg *ast.Package, arg ast.Expr) []string {
	var calledFuncs []string
	switch argExpr := arg.(type) {
	case *ast.Ident:
		// If the argument is an identifier, check if it's a global variable or function
		if _, exists := packageVars[argExpr.Name]; exists {
			markVarAccessed(pkg, argExpr.Name)
		}
	case *ast.CallExpr:
		// If the argument is a call expression, inspect the function being called and its arguments
		if funcIdent, ok := argExpr.Fun.(*ast.Ident); ok {
			calledFuncs = append(calledFuncs, funcIdent.Name)
			for _, nestedArg := range argExpr.Args {
				inspectCallArg(pkg, nestedArg)
			}
		}
	}
	return calledFuncs
}

func collectCalledFunctions(pkg *ast.Package, body *ast.BlockStmt) []string {
	var calledFuncs []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch call := n.(type) {
		case *ast.CallExpr:
			if ident, ok := call.Fun.(*ast.Ident); ok {
				calledFuncs = append(calledFuncs, ident.Name)
			}

			for _, arg := range call.Args {
				calledFuncs = append(calledFuncs, inspectCallArg(pkg, arg)...)
			}

		case *ast.Ident:
			// Check if the identifier is a package-level variable
			if genDecl, exists := packageVars[call.Name]; exists {
				if valueSpec, ok := genDecl.Specs[0].(*ast.ValueSpec); ok {
					// Ensure that the identifier refers to the package-level variable and not a local variable
					if valueSpec.Names[0].Obj == call.Obj {
						markVarAccessed(pkg, call.Name)
					}
				}
			}
		}
		return true
	})
	return calledFuncs
}

func markVarAccessed(pkg *ast.Package, accessed string) {
	if _, exists := accessedVars[accessed]; exists {
		return
	}

	accessedVars[accessed] = true

	genDecl, ok := packageVars[accessed]
	if !ok {
		return
	}

	for _, spec := range genDecl.Specs {
		if valueSpec, ok := spec.(*ast.ValueSpec); ok {
			for _, value := range valueSpec.Values {
				ast.Inspect(value, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.Ident:
						// Check if the identifier is a package-level variable
						if _, exists := packageVars[v.Name]; exists {
							markVarAccessed(pkg, v.Name)
						}
					case *ast.CallExpr:
						// Inspect the arguments of the function call
						if funIdent, ok := v.Fun.(*ast.Ident); ok {
							// This is a call to a function; add it to the list of called functions
							// We assume it's a top-level function for simplicity
							collectFunction(pkg, funIdent.Name, 0)
						}

						for _, arg := range v.Args {
							newFuncs := inspectCallArg(pkg, arg)
							for _, f := range newFuncs {
								collectFunction(pkg, f, 0)
							}
						}
					}
					return true
				})
			}
		}
	}
}

func collectPackageVars(pkg *ast.Package) {
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			if genDecl, ok := n.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
				for _, spec := range genDecl.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range valueSpec.Names {
							packageVars[name.Name] = genDecl
						}
					}
				}
			}
			return true
		})
	}
}

func copyFile(src, testName string, subDirs ...string) error {
	fileName := filepath.Base(src)

	// Construct the directory path using testName and subDirs
	testStep := fmt.Sprint(testMetaData[testName])
	paths := []string{"testdata", testName, testStep}
	paths = append(paths, subDirs...) // append subdirectories if any
	dirPath := filepath.Join(paths...)
	ensureDir(dirPath)

	// Construct the full destination path for the file
	destPath := filepath.Join(dirPath, fileName)

	// Check if dest file already exists; if yes, just return
	if _, err := os.Stat(destPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	// Open source file for reading
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create and open dest file for writing
	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy from src to dest
	if _, err := io.Copy(destFile, srcFile); err != nil {
		return err
	}

	return nil
}

// Checks if the function is a read function, if so returns the resource
// that this read function belongs to
func isValidReadFunction(functionName string) (string, bool) {
	resourceType, exists := validReadFunctions[functionName]
	return resourceType, exists
}

func findCallerWithSubstring() (resourceType string, found bool) {
	pc := make([]uintptr, 30)
	n := runtime.Callers(0, pc)
	if n == 0 {
		return
	}

	pc = pc[:n]
	frames := runtime.CallersFrames(pc)

	// Loop over the frames to find the desired function name substring
	for {
		frame, more := frames.Next()
		fmt.Println(frame.Function)
		resourceType, validReadFunction := isValidReadFunction(frame.Function)
		if validReadFunction {
			// Check next immediate parent
			frame2, more2 := frames.Next()
			fmt.Println(frame2.Function)
			// There are two functions that will call read.
			// One is the Create function, the next is the SDK to refresh state. We only care about the
			// sdk refresh.
			if more2 && strings.Contains(frame2.Function, "schema.(*Resource).read") {
				return resourceType, true
			}
		}
		if !more {
			break
		}
	}

	return "", false
}

type loggingRoundTripper struct {
	underlying http.RoundTripper
	testName   string
}

// RoundTrip logs the request and then delegates to the underlying RoundTripper
func (lrt *loggingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	startTime := time.Now()
	fmt.Println("making request: " + r.URL.String())
	resp, err := lrt.underlying.RoundTrip(r)
	duration := time.Since(startTime)
	resourceType, callerfound := findCallerWithSubstring()
	if callerfound {
		if err != nil {
			logTestdata(lrt.testName, "requests.log", fmt.Sprintf("HTTP Request failed: %s %s. Duration: %v. Error: %s\n\n", r.Method, r.URL, duration, err), resourceType)
		} else {
			logTestdata(lrt.testName, "requests.log", fmt.Sprintf("HTTP Request: %s %s. Duration: %v. Response status: %d\n%s\n\n", r.Method, r.URL, duration, resp.StatusCode, resp.Body), resourceType)
		}
	}
	return resp, err
}
