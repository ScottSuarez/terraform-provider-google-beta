// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5/tf5server"
	"github.com/hashicorp/terraform-plugin-mux/tf5muxserver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"gopkg.in/yaml.v3"

	"github.com/hashicorp/terraform-provider-google-beta/google-beta/filelogger"
	"github.com/hashicorp/terraform-provider-google-beta/google-beta/fwprovider"
	"github.com/hashicorp/terraform-provider-google-beta/google-beta/provider"
	ver "github.com/hashicorp/terraform-provider-google-beta/version"
)

var (
	// these will be set by the goreleaser configuration
	// to appropriate values for the compiled binary
	version string = ver.ProviderVersion

	// goreleaser can also pass the specific commit if you want
	// commit  string = ""
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	fl := filelogger.NewFileLogger("schema")

	for name, r := range provider.GENERATED_RESOURCES {
		yamlSchema := createMinimalResourceSchema(r.Schema, "resource", name)
		fl.LogData("resource", name+".yaml", yamlSchema)

		jsonSchema := createMinimalResourceSchemaJSON(r.Schema, "resource", name)
		fl.LogData("resource", name+".json", jsonSchema)
	}

	for name, r := range provider.GENERATED_DATASOURCES {
		yamlSchema := createMinimalResourceSchema(r.Schema, "datasource", name)
		fl.LogData("datasource", name+".yaml", yamlSchema)

		jsonSchema := createMinimalResourceSchemaJSON(r.Schema, "datasource", name)
		fl.LogData("datasource", name+".json", jsonSchema)
	}

	// concat with sdkv2 provider
	providers := []func() tfprotov5.ProviderServer{
		providerserver.NewProtocol5(fwprovider.New(version)), // framework provider
		provider.Provider().GRPCProvider,                     // sdk provider
	}

	// use the muxer
	muxServer, err := tf5muxserver.NewMuxServer(context.Background(), providers...)
	if err != nil {
		log.Fatalf(err.Error())
	}

	var serveOpts []tf5server.ServeOpt

	if debug {
		serveOpts = append(serveOpts, tf5server.WithManagedDebug())
	}

	err = tf5server.Serve(
		"registry.terraform.io/hashicorp/google-beta",
		muxServer.ProviderServer,
		serveOpts...,
	)

	if err != nil {
		log.Fatal(err)
	}
}

func createMinimalResourceSchema(resourceSchema map[string]*schema.Schema, schemaType, resourceName string) string {

	yamlNode := &yaml.Node{}
	err := schemaToYAMLHierarchy(yamlNode, resourceSchema)
	if err != nil {
		log.Fatalf("Error generating YAML hierarchy: %s", err)
	}

	yamlBytes, err := yaml.Marshal(yamlNode)
	if err != nil {
		log.Fatalf("Error marshaling YAML: %s", err)
	}

	return string(yamlBytes)
}

func schemaToYAMLHierarchy(node *yaml.Node, schemaObj map[string]*schema.Schema) error {
	node.Kind = yaml.MappingNode
	node.Tag = "!!map"

	// Sorting keys
	keys := make([]string, 0, len(schemaObj))
	for k := range schemaObj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := schemaObj[k]
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: k,
		}

		valueNode := &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}

		// Add Type
		appendYAMLNode(valueNode, "Type", v.Type.String())

		// Add Description
		if v.Description != "" {
			appendYAMLNode(valueNode, "Description", v.Description)
		}

		// Add other parameters only if they have significant values
		if v.Required {
			appendYAMLNode(valueNode, "Required", "true")
		}
		if v.Computed {
			appendYAMLNode(valueNode, "Computed", "true")
		}
		if v.Optional {
			appendYAMLNode(valueNode, "Optional", "true")
		}

		// Checking and adding additional parameters
		addOptionalParameters(valueNode, v)

		if _, isResource := v.Elem.(*schema.Resource); isResource {
			subNode := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
			}
			err := schemaToYAMLHierarchy(subNode, v.Elem.(*schema.Resource).Schema)
			if err != nil {
				return err
			}
			valueNode.Content = append(valueNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Properties"}, subNode)
		}

		node.Content = append(node.Content, keyNode, valueNode)
	}

	return nil
}

func appendYAMLNode(parent *yaml.Node, key, value string) {
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func addOptionalParameters(node *yaml.Node, fieldSchema *schema.Schema) {
	// Adding new parameters with checks for non-zero or non-empty values
	if len(fieldSchema.RequiredWith) > 0 {
		appendYAMLNode(node, "RequiredWith", strings.Join(fieldSchema.RequiredWith, ", "))
	}
	if len(fieldSchema.AtLeastOneOf) > 0 {
		appendYAMLNode(node, "AtLeastOneOf", strings.Join(fieldSchema.AtLeastOneOf, ", "))
	}
	if len(fieldSchema.ExactlyOneOf) > 0 {
		appendYAMLNode(node, "ExactlyOneOf", strings.Join(fieldSchema.ExactlyOneOf, ", "))
	}
	if len(fieldSchema.ConflictsWith) > 0 {
		appendYAMLNode(node, "ConflictsWith", strings.Join(fieldSchema.ConflictsWith, ", "))
	}
	if fieldSchema.MaxItems > 0 {
		appendYAMLNode(node, "MaxItems", strconv.Itoa(fieldSchema.MaxItems))
	}
	if fieldSchema.MinItems > 0 {
		appendYAMLNode(node, "MinItems", strconv.Itoa(fieldSchema.MinItems))
	}
}

func createMinimalResourceSchemaJSON(resourceSchema map[string]*schema.Schema, schemaType, resourceName string) string {
	jsonMap := make(map[string]interface{})
	err := schemaToJSONHierarchy(jsonMap, resourceSchema)
	if err != nil {
		log.Fatalf("Error generating JSON hierarchy: %s", err)
	}

	jsonBytes, err := json.MarshalIndent(jsonMap, "", "    ")
	if err != nil {
		log.Fatalf("Error marshaling JSON: %s", err)
	}

	return string(jsonBytes)
}

func schemaToJSONHierarchy(jsonObj map[string]interface{}, schemaObj map[string]*schema.Schema) error {
	for k, v := range schemaObj {
		valueMap := make(map[string]interface{})
		valueMap["Type"] = v.Type.String()

		if v.Description != "" {
			valueMap["Description"] = v.Description
		}

		if v.Required {
			valueMap["Required"] = true
		}
		if v.Computed {
			valueMap["Computed"] = true
		}
		if v.Optional {
			valueMap["Optional"] = true
		}

		addOptionalParametersJSON(valueMap, v)

		if _, isResource := v.Elem.(*schema.Resource); isResource {
			subMap := make(map[string]interface{})
			err := schemaToJSONHierarchy(subMap, v.Elem.(*schema.Resource).Schema)
			if err != nil {
				return err
			}
			valueMap["Properties"] = subMap
		}

		jsonObj[k] = valueMap
	}
	return nil
}

func addOptionalParametersJSON(jsonObj map[string]interface{}, fieldSchema *schema.Schema) {
	if len(fieldSchema.RequiredWith) > 0 {
		jsonObj["RequiredWith"] = strings.Join(fieldSchema.RequiredWith, ", ")
	}
	if len(fieldSchema.AtLeastOneOf) > 0 {
		jsonObj["AtLeastOneOf"] = strings.Join(fieldSchema.AtLeastOneOf, ", ")
	}
	if len(fieldSchema.ExactlyOneOf) > 0 {
		jsonObj["ExactlyOneOf"] = strings.Join(fieldSchema.ExactlyOneOf, ", ")
	}
	if len(fieldSchema.ConflictsWith) > 0 {
		jsonObj["ConflictsWith"] = strings.Join(fieldSchema.ConflictsWith, ", ")
	}
	if fieldSchema.MaxItems > 0 {
		jsonObj["MaxItems"] = fieldSchema.MaxItems
	}
	if fieldSchema.MinItems > 0 {
		jsonObj["MinItems"] = fieldSchema.MinItems
	}
}
