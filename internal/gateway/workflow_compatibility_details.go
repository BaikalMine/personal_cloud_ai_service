package gateway

import (
	"net/url"
	"sort"
	"strings"
)

type workflowCompatibilityComponent struct {
	ClassType string
	Module    string
	Present   bool
}

type workflowCompatibilityBranch struct {
	Name       string
	Selected   bool
	Present    int
	Components []workflowCompatibilityComponent
}

func compatibilityComponents(classes []string, schema comfySchemaCatalog) []workflowCompatibilityComponent {
	seen := make(map[string]bool)
	components := make([]workflowCompatibilityComponent, 0, len(classes))
	for _, class := range classes {
		if class == "" || seen[class] {
			continue
		}
		seen[class] = true
		node, present := schema.Nodes[class]
		components = append(components, workflowCompatibilityComponent{ClassType: class, Module: node.PythonModule, Present: present})
	}
	sort.Slice(components, func(i, j int) bool {
		if components[i].Present != components[j].Present {
			return !components[i].Present
		}
		return components[i].ClassType < components[j].ClassType
	})
	return components
}

func (result *workflowCompatibilityResult) addComponentDetails(scenario workflowCompatibilityScenario, schema comfySchemaCatalog, prompt map[string]any) {
	result.DefinitionID = scenario.Definition.ID
	classes := make([]string, 0, len(prompt))
	for _, raw := range prompt {
		if node, ok := promptNode(raw); ok {
			class, _ := node["class_type"].(string)
			classes = append(classes, class)
		}
	}
	// Every node in this assembled scenario is required, even when it belongs
	// to an option. Inactive options never change the scenario's preflight.
	result.Components = compatibilityComponents(classes, schema)
	result.Files = generationExecutionComponents(prompt)
	manifest, exists := workflowManifestByID(scenario.Definition.ID)
	if !exists {
		return
	}
	result.ManifestName, result.ManifestVersion = manifest.Name, manifest.Version
	for _, branch := range manifest.Branches {
		if branch.ToggleParameter == "" && len(branch.Conditions) == 0 {
			continue
		}
		selected := workflowConditionsMatch(manifest, scenario.Input, branch.Conditions)
		if branch.ToggleParameter != "" {
			value, ok := workflowConditionValue(manifest, scenario.Input, branch.ToggleParameter)
			selected = selected && ok && choiceEqual(value, true)
		}
		detail := workflowCompatibilityBranch{
			Name: branch.Name, Selected: selected,
			Components: compatibilityComponents(branch.RequiredClasses, schema),
		}
		for _, component := range detail.Components {
			if component.Present {
				detail.Present++
			}
		}
		result.Branches = append(result.Branches, detail)
	}
}

type workflowCompatibilityView struct {
	Query    string
	State    string
	Family   string
	Families []string
	Results  []workflowCompatibilityResult
	Total    int
}

func newWorkflowCompatibilityView(results []workflowCompatibilityResult, query url.Values) workflowCompatibilityView {
	view := workflowCompatibilityView{Query: strings.TrimSpace(query.Get("q")), State: query.Get("state"), Total: len(results)}
	if text := []rune(view.Query); len(text) > 200 {
		view.Query = string(text[:200])
	}
	if view.State != workflowCompatibilityOK && view.State != workflowCompatibilityErrorStatus && view.State != workflowCompatibilityUnavailable {
		view.State = ""
	}
	families := make(map[string]bool)
	for _, result := range results {
		if result.Family != "" {
			families[result.Family] = true
		}
	}
	for family := range families {
		view.Families = append(view.Families, family)
	}
	sort.Strings(view.Families)
	if families[query.Get("family")] {
		view.Family = query.Get("family")
	}
	needle := strings.ToLower(view.Query)
	for _, result := range results {
		if view.State != "" && result.Status != view.State || view.Family != "" && result.Family != view.Family {
			continue
		}
		parts := []string{result.Scenario, result.Description, result.Model, result.ModelFile, result.Family, result.DefinitionID, result.ManifestName, result.ManifestVersion}
		for _, issue := range result.Issues {
			parts = append(parts, issue.Label, issue.Message, issue.ClassType, issue.InputName)
		}
		for _, component := range result.Components {
			parts = append(parts, component.ClassType, component.Module)
		}
		for _, file := range result.Files {
			parts = append(parts, file.Name, file.Class, file.Field)
		}
		for _, branch := range result.Branches {
			parts = append(parts, branch.Name)
			for _, component := range branch.Components {
				parts = append(parts, component.ClassType, component.Module)
			}
		}
		if strings.Contains(strings.ToLower(strings.Join(parts, "\n")), needle) {
			view.Results = append(view.Results, result)
		}
	}
	return view
}
