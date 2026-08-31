package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const nugetOrgSource = "https://api.nuget.org/v3/index.json"

type msbuildNode struct {
	name     string
	attrs    map[string]string
	text     strings.Builder
	children []*msbuildNode
	parent   *msbuildNode
}

type nugetReference struct {
	name          string
	version       string
	includeAssets string
	excludeAssets string
	privateAssets string
}

func sanitizedNuGetProject(worktree, cacheRoot, project string) (string, string, string, error) {
	projects := map[string]string{}
	synthetic, err := sanitizeNuGetProjectGraph(worktree, cacheRoot, project, projects)
	if err != nil {
		return "", "", "", err
	}
	directory := filepath.Dir(synthetic)
	generatedLock := filepath.Join(directory, "packages.lock.json")
	configFile := filepath.Join(cacheRoot, "nuget-projects", "NuGet.Config")
	if err := os.MkdirAll(filepath.Dir(configFile), 0o700); err != nil {
		return "", "", "", err
	}
	configuration := []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n    <add key=\"nuget.org\" value=\"" + nugetOrgSource + "\" protocolVersion=\"3\" />\n  </packageSources>\n</configuration>\n")
	if err := os.WriteFile(configFile, configuration, 0o600); err != nil {
		return "", "", "", err
	}
	return synthetic, generatedLock, configFile, nil
}

func sanitizeNuGetProjectGraph(worktree, cacheRoot, project string, projects map[string]string) (string, error) {
	if synthetic := projects[project]; synthetic != "" {
		return synthetic, nil
	}
	projectPath, err := containedPath(worktree, project)
	if err != nil {
		return "", err
	}
	root, err := parseMSBuildFile(projectPath)
	if err != nil {
		return "", fmt.Errorf("sanitize NuGet project %s: %w", project, err)
	}
	if !strings.EqualFold(root.name, "Project") || root.attrs["sdk"] != "Microsoft.NET.Sdk" {
		return "", fmt.Errorf("sanitize NuGet project %s: only Project Sdk=\"Microsoft.NET.Sdk\" is supported", project)
	}
	properties, err := nugetProperties(root)
	if err != nil {
		return "", fmt.Errorf("sanitize NuGet project %s: %w", project, err)
	}
	if properties["targetframework"] == "" && properties["targetframeworks"] == "" {
		return "", fmt.Errorf("sanitize NuGet project %s: a literal TargetFramework or TargetFrameworks is required", project)
	}
	central, err := centralNuGetVersions(worktree, cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(project))))
	if err != nil {
		return "", fmt.Errorf("sanitize NuGet project %s: %w", project, err)
	}
	references, err := nugetReferences(root, central)
	if err != nil {
		return "", fmt.Errorf("sanitize NuGet project %s: %w", project, err)
	}
	if len(references) == 0 {
		return "", fmt.Errorf("sanitize NuGet project %s: no static PackageReference entries found", project)
	}

	digest := sha256.Sum256([]byte(project))
	directory := filepath.Join(cacheRoot, "nuget-projects", hex.EncodeToString(digest[:8]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	synthetic := filepath.Join(directory, filepath.Base(filepath.FromSlash(project)))
	projects[project] = synthetic
	projectReferences, err := nugetProjectReferences(worktree, project, root)
	if err != nil {
		return "", fmt.Errorf("sanitize NuGet project %s: %w", project, err)
	}
	var syntheticReferences []string
	for _, reference := range projectReferences {
		syntheticReference, err := sanitizeNuGetProjectGraph(worktree, cacheRoot, reference, projects)
		if err != nil {
			return "", err
		}
		syntheticReferences = append(syntheticReferences, syntheticReference)
	}
	if err := os.WriteFile(synthetic, renderNuGetProject(properties, references, syntheticReferences), 0o600); err != nil {
		return "", err
	}
	return synthetic, nil
}

func parseMSBuildFile(path string) (*msbuildNode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxGeneratedFile {
		return nil, fmt.Errorf("MSBuild input must be a regular file no larger than 64 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, maxGeneratedFile+1))
	var stack []*msbuildNode
	var root *msbuildNode
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			node := &msbuildNode{name: value.Name.Local, attrs: map[string]string{}}
			for _, attribute := range value.Attr {
				name := strings.ToLower(attribute.Name.Local)
				if _, exists := node.attrs[name]; exists {
					return nil, fmt.Errorf("duplicate XML attribute %s", attribute.Name.Local)
				}
				node.attrs[name] = attribute.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("multiple XML roots")
				}
				root = node
			} else {
				node.parent = stack[len(stack)-1]
				node.parent.children = append(node.parent.children, node)
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) > 0 {
				_, _ = stack[len(stack)-1].text.Write(value)
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != value.Name.Local {
				return nil, errors.New("malformed XML nesting")
			}
			stack = stack[:len(stack)-1]
		case xml.Directive:
			return nil, errors.New("XML directives are not supported")
		case xml.ProcInst:
			if !strings.EqualFold(value.Target, "xml") {
				return nil, errors.New("XML processing instructions are not supported")
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("missing or incomplete XML root")
	}
	return root, nil
}

func nugetProperties(root *msbuildNode) (map[string]string, error) {
	wanted := map[string]bool{
		"targetframework": true, "targetframeworks": true,
		"runtimeidentifier": true, "runtimeidentifiers": true,
		"assemblyname": true,
	}
	properties := map[string]string{}
	for _, group := range root.children {
		if !strings.EqualFold(group.name, "PropertyGroup") {
			continue
		}
		for _, property := range group.children {
			key := strings.ToLower(property.name)
			if !wanted[key] {
				continue
			}
			if condition := strings.TrimSpace(group.attrs["condition"] + property.attrs["condition"]); condition != "" {
				return nil, fmt.Errorf("conditional %s is not supported", property.name)
			}
			value := strings.TrimSpace(property.text.String())
			if err := literalMSBuildValue(property.name, value); err != nil {
				return nil, err
			}
			if previous := properties[key]; previous != "" && previous != value {
				return nil, fmt.Errorf("conflicting %s values", property.name)
			}
			properties[key] = value
		}
	}
	if properties["targetframework"] != "" && properties["targetframeworks"] != "" {
		return nil, errors.New("both TargetFramework and TargetFrameworks are defined")
	}
	return properties, nil
}

func centralNuGetVersions(worktree, directory string) (map[string]string, error) {
	manifest, ok, err := nearestFile(worktree, directory, "Directory.Packages.props")
	if err != nil || !ok {
		return map[string]string{}, err
	}
	path, err := containedPath(worktree, manifest)
	if err != nil {
		return nil, err
	}
	root, err := parseMSBuildFile(path)
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for _, node := range descendants(root, "PackageVersion") {
		if node.parent == nil || !strings.EqualFold(node.parent.name, "ItemGroup") || node.parent.parent != root {
			return nil, errors.New("PackageVersion must be a direct child of a project ItemGroup")
		}
		name := strings.TrimSpace(firstNonempty(node.attrs["include"], node.attrs["update"]))
		version := strings.TrimSpace(node.attrs["version"])
		if version == "" {
			version, err = safeChildText(node, "Version")
			if err != nil {
				return nil, err
			}
		}
		if condition := inheritedCondition(node, root); condition != "" {
			return nil, fmt.Errorf("conditional central PackageVersion %s is not supported", name)
		}
		if err := literalPackage(name, version); err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if previous := versions[key]; previous != "" && previous != version {
			return nil, fmt.Errorf("conflicting central versions for %s", name)
		}
		versions[key] = version
	}
	return versions, nil
}

func nugetReferences(root *msbuildNode, central map[string]string) ([]nugetReference, error) {
	versions := map[string]nugetReference{}
	for _, node := range descendants(root, "PackageReference") {
		if node.parent == nil || !strings.EqualFold(node.parent.name, "ItemGroup") || node.parent.parent != root {
			return nil, errors.New("PackageReference must be a direct child of a project ItemGroup")
		}
		name := strings.TrimSpace(firstNonempty(node.attrs["include"], node.attrs["update"]))
		version := strings.TrimSpace(node.attrs["version"])
		if version == "" {
			childVersion, err := safeChildText(node, "Version")
			if err != nil {
				return nil, err
			}
			version = childVersion
		}
		if version == "" {
			childVersion, err := safeChildText(node, "VersionOverride")
			if err != nil {
				return nil, err
			}
			version = childVersion
		}
		if version == "" {
			version = central[strings.ToLower(name)]
		}
		if condition := inheritedCondition(node, root); condition != "" {
			return nil, fmt.Errorf("conditional PackageReference %s is not supported", name)
		}
		if err := literalPackage(name, version); err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if previous, ok := versions[key]; ok && previous.version != version {
			return nil, fmt.Errorf("conflicting versions for PackageReference %s", name)
		}
		includeAssets, err := nugetMetadata(node, "IncludeAssets")
		if err != nil {
			return nil, err
		}
		excludeAssets, err := nugetMetadata(node, "ExcludeAssets")
		if err != nil {
			return nil, err
		}
		privateAssets, err := nugetMetadata(node, "PrivateAssets")
		if err != nil {
			return nil, err
		}
		versions[key] = nugetReference{
			name: name, version: version, includeAssets: includeAssets,
			excludeAssets: excludeAssets, privateAssets: privateAssets,
		}
	}
	result := make([]nugetReference, 0, len(versions))
	for _, reference := range versions {
		result = append(result, reference)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].name) < strings.ToLower(result[j].name) })
	return result, nil
}

func nugetMetadata(node *msbuildNode, name string) (string, error) {
	value := strings.TrimSpace(node.attrs[strings.ToLower(name)])
	if value == "" {
		var err error
		value, err = safeChildText(node, name)
		if err != nil {
			return "", err
		}
	}
	if value == "" {
		return "", nil
	}
	if err := literalMSBuildValue("PackageReference "+name, value); err != nil {
		return "", err
	}
	return value, nil
}

func nugetProjectReferences(worktree, project string, root *msbuildNode) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, node := range descendants(root, "ProjectReference") {
		if node.parent == nil || !strings.EqualFold(node.parent.name, "ItemGroup") || node.parent.parent != root {
			return nil, errors.New("ProjectReference must be a direct child of a project ItemGroup")
		}
		if condition := inheritedCondition(node, root); condition != "" {
			return nil, errors.New("conditional ProjectReference is not supported")
		}
		include := strings.TrimSpace(node.attrs["include"])
		if err := literalMSBuildValue("ProjectReference Include", include); err != nil {
			return nil, err
		}
		include = strings.ReplaceAll(include, `\`, "/")
		candidate := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(project)), filepath.FromSlash(include))))
		path, err := containedPath(worktree, candidate)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".csproj") {
			return nil, fmt.Errorf("ProjectReference %s is not a regular .csproj", include)
		}
		if !seen[candidate] {
			seen[candidate] = true
			result = append(result, candidate)
		}
	}
	sort.Strings(result)
	return result, nil
}

func descendants(root *msbuildNode, name string) []*msbuildNode {
	var result []*msbuildNode
	var visit func(*msbuildNode)
	visit = func(node *msbuildNode) {
		if strings.EqualFold(node.name, name) {
			result = append(result, node)
		}
		for _, child := range node.children {
			visit(child)
		}
	}
	visit(root)
	return result
}

func safeChildText(node *msbuildNode, name string) (string, error) {
	for _, child := range node.children {
		if strings.EqualFold(child.name, name) {
			if condition := strings.TrimSpace(child.attrs["condition"]); condition != "" {
				return "", fmt.Errorf("conditional %s for %s is not supported", name, node.attrs["include"])
			}
			return strings.TrimSpace(child.text.String()), nil
		}
	}
	return "", nil
}

func inheritedCondition(node, stop *msbuildNode) string {
	for current := node; current != nil && current != stop; current = current.parent {
		if condition := strings.TrimSpace(current.attrs["condition"]); condition != "" {
			return condition
		}
	}
	return ""
}

func literalMSBuildValue(name, value string) error {
	if value == "" || strings.ContainsAny(value, "\r\n") || strings.Contains(value, "$(") || strings.Contains(value, "@(") || strings.Contains(value, "%(") {
		return fmt.Errorf("%s must be a non-empty literal value", name)
	}
	return nil
}

func literalPackage(name, version string) error {
	if err := literalMSBuildValue("PackageReference name", name); err != nil {
		return err
	}
	if err := literalMSBuildValue("PackageReference "+name+" version", version); err != nil {
		return err
	}
	return nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func renderNuGetProject(properties map[string]string, references []nugetReference, projectReferences []string) []byte {
	var output bytes.Buffer
	output.WriteString("<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n")
	for _, property := range []struct{ key, name string }{
		{"targetframework", "TargetFramework"},
		{"targetframeworks", "TargetFrameworks"},
		{"runtimeidentifier", "RuntimeIdentifier"},
		{"runtimeidentifiers", "RuntimeIdentifiers"},
		{"assemblyname", "AssemblyName"},
	} {
		if value := properties[property.key]; value != "" {
			fmt.Fprintf(&output, "    <%s>", property.name)
			_ = xml.EscapeText(&output, []byte(value))
			fmt.Fprintf(&output, "</%s>\n", property.name)
		}
	}
	output.WriteString("    <RestorePackagesWithLockFile>true</RestorePackagesWithLockFile>\n    <NuGetAudit>false</NuGetAudit>\n  </PropertyGroup>\n  <ItemGroup>\n")
	for _, reference := range references {
		output.WriteString("    <PackageReference Include=\"")
		_ = xml.EscapeText(&output, []byte(reference.name))
		output.WriteString("\" Version=\"")
		_ = xml.EscapeText(&output, []byte(reference.version))
		output.WriteString("\"")
		for _, attribute := range []struct{ name, value string }{
			{"IncludeAssets", reference.includeAssets},
			{"ExcludeAssets", reference.excludeAssets},
			{"PrivateAssets", reference.privateAssets},
		} {
			if attribute.value != "" {
				fmt.Fprintf(&output, " %s=\"", attribute.name)
				_ = xml.EscapeText(&output, []byte(attribute.value))
				output.WriteString("\"")
			}
		}
		output.WriteString(" />\n")
	}
	for _, reference := range projectReferences {
		output.WriteString("    <ProjectReference Include=\"")
		_ = xml.EscapeText(&output, []byte(reference))
		output.WriteString("\" />\n")
	}
	output.WriteString("  </ItemGroup>\n</Project>\n")
	return output.Bytes()
}
