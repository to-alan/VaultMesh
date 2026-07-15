package control

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOpenAPIContractCoversEveryRegisteredRoute(t *testing.T) {
	handlerSource, err := os.ReadFile("http.go")
	if err != nil {
		t.Fatal(err)
	}
	contractSource, err := os.Open("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer contractSource.Close()

	registered := map[string]struct{}{}
	routePattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) ([^"]+)"`)
	for _, match := range routePattern.FindAllStringSubmatch(string(handlerSource), -1) {
		registered[match[1]+" "+match[2]] = struct{}{}
	}

	documented := map[string]struct{}{}
	pathPattern := regexp.MustCompile(`^  (/[^:]+):\s*$`)
	methodPattern := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	currentPath := ""
	scanner := bufio.NewScanner(contractSource)
	for scanner.Scan() {
		line := scanner.Text()
		if match := pathPattern.FindStringSubmatch(line); match != nil {
			currentPath = match[1]
			continue
		}
		if currentPath != "" {
			if match := methodPattern.FindStringSubmatch(line); match != nil {
				documented[strings.ToUpper(match[1])+" "+currentPath] = struct{}{}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	for route := range registered {
		if _, ok := documented[route]; !ok {
			t.Errorf("registered route %s is missing from docs/openapi.yaml", route)
		}
	}
	for route := range documented {
		if _, ok := registered[route]; !ok {
			t.Errorf("OpenAPI operation %s is not registered by HTTPServer.Handler", route)
		}
	}
	if len(registered) == 0 || len(documented) == 0 {
		t.Fatalf("route extraction failed: registered=%d documented=%d", len(registered), len(documented))
	}

	contractBytes, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	operationPattern := regexp.MustCompile(`(?m)^      operationId: ([A-Za-z][A-Za-z0-9]*)\s*$`)
	operationIDs := map[string]struct{}{}
	for _, match := range operationPattern.FindAllStringSubmatch(string(contractBytes), -1) {
		if _, duplicated := operationIDs[match[1]]; duplicated {
			t.Errorf("OpenAPI operationId %q is duplicated", match[1])
		}
		operationIDs[match[1]] = struct{}{}
	}
	if len(operationIDs) != len(documented) {
		t.Errorf("each documented operation must have one unique operationId: operations=%d operationIds=%d", len(documented), len(operationIDs))
	}

	definitions := map[string]struct{}{}
	componentGroup := ""
	inComponents := false
	groupPattern := regexp.MustCompile(`^  ([A-Za-z][A-Za-z0-9]*):\s*$`)
	definitionPattern := regexp.MustCompile(`^    ([A-Za-z][A-Za-z0-9_-]*):\s*$`)
	for _, line := range strings.Split(string(contractBytes), "\n") {
		if line == "components:" {
			inComponents = true
			continue
		}
		if !inComponents {
			continue
		}
		if match := groupPattern.FindStringSubmatch(line); match != nil {
			componentGroup = match[1]
			continue
		}
		if componentGroup != "" {
			if match := definitionPattern.FindStringSubmatch(line); match != nil {
				definitions["#/components/"+componentGroup+"/"+match[1]] = struct{}{}
			}
		}
	}
	referencePattern := regexp.MustCompile(`#/components/[A-Za-z][A-Za-z0-9]*/[A-Za-z][A-Za-z0-9_-]*`)
	for _, reference := range referencePattern.FindAllString(string(contractBytes), -1) {
		if _, ok := definitions[reference]; !ok {
			t.Errorf("OpenAPI reference %s has no component definition", reference)
		}
	}
}
