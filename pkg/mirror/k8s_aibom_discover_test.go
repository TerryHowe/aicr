// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mirror

import (
	"context"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/aicr/pkg/helm/helmtest"
	"github.com/NVIDIA/aicr/pkg/recipe"
)

// TestK8sAIBOM_MirrorHandsQualifiedCoordinatesToRenderer covers the AICR-owned
// half of ADR-019's air-gap requirement.
//
// The requirement reads "aicr mirror list discovers exactly
// ghcr.io/googlecloudplatform/k8s-aibom@sha256:...". That claim decomposes:
// AICR is responsible for hydrating the component's values file and handing
// the renderer the qualified, digest-pinned coordinates; the upstream chart is
// responsible for turning those values into an image reference. Only the first
// half is AICR's to test, and only the first half can be tested without
// pulling the real chart over the network.
//
// A MockRenderer here would let us assert on canned output we wrote ourselves,
// which proves nothing. Asserting the ChartInput the Lister actually built is
// the real invariant: if the values file stops being hydrated, if the registry
// coordinates drift, or if the digest stops reaching the renderer, this fails.
// The end-to-end "the rendered chart yields exactly one digest-pinned image"
// claim is asserted against the real chart by tools/k8s-aibom-test.
func TestK8sAIBOM_MirrorHandsQualifiedCoordinatesToRenderer(t *testing.T) {
	ctx := context.Background()

	registry, err := recipe.GetComponentRegistry()
	if err != nil {
		t.Fatalf("GetComponentRegistry: %v", err)
	}
	component := registry.Get("k8s-aibom")
	if component == nil {
		t.Fatal("k8s-aibom is not in the component registry")
	}

	provider := recipe.NewEmbeddedDataProvider(recipe.GetEmbeddedFS(), "")
	rec := &recipe.RecipeResult{
		Kind:       recipe.RecipeResultKind,
		APIVersion: "aicr.run/v1alpha2",
		ComponentRefs: []recipe.ComponentRef{{
			Name:       "k8s-aibom",
			Namespace:  component.Helm.DefaultNamespace,
			Type:       recipe.ComponentTypeHelm,
			Source:     component.Helm.DefaultRepository,
			Chart:      component.Helm.DefaultChart,
			Version:    component.Helm.DefaultVersion,
			ValuesFile: "components/k8s-aibom/values.yaml",
		}},
	}
	rec.BindDataProvider(provider)

	renderer := &helmtest.MockRenderer{}
	lister := NewLister(WithHelmRenderer(renderer), WithVersion("v1.0.0"))

	list, err := lister.Discover(ctx, rec)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Exactly one chart, at the registry's qualified coordinates. Asserting
	// the count (not just "a matching entry exists") is what makes a stray
	// second chart a failure rather than a silent pass.
	if len(list.Charts) != 1 {
		t.Fatalf("discovered %d charts, want exactly 1: %+v", len(list.Charts), list.Charts)
	}
	chart := list.Charts[0]
	if chart.Repository != component.Helm.DefaultRepository ||
		chart.Chart != component.Helm.DefaultChart ||
		chart.Version != component.Helm.DefaultVersion {

		t.Errorf("chart = %+v, want repository=%s chart=%s version=%s",
			chart, component.Helm.DefaultRepository, component.Helm.DefaultChart,
			component.Helm.DefaultVersion)
	}

	if len(renderer.Inputs) != 1 {
		t.Fatalf("renderer received %d inputs, want exactly 1", len(renderer.Inputs))
	}
	input := renderer.Inputs[0]
	if input.Chart != component.Helm.DefaultChart || input.Repository != component.Helm.DefaultRepository {
		t.Errorf("ChartInput chart/repository = %s/%s, want %s/%s",
			input.Chart, input.Repository, component.Helm.DefaultChart, component.Helm.DefaultRepository)
	}

	// The digest must survive values hydration and reach the renderer. This is
	// the load-bearing assertion: an unhydrated values file, or a values file
	// that lost its digest pin, would leave the mirror inventory referencing a
	// floating tag and silently break air-gap relocation.
	wantDigest := qualifiedAIBOMDigest(t, provider)
	image, ok := input.Values["image"].(map[string]any)
	if !ok {
		t.Fatalf("ChartInput.Values has no image mapping: %+v", input.Values)
	}
	if got := image["digest"]; got != wantDigest {
		t.Errorf("ChartInput.Values.image.digest = %v, want %s", got, wantDigest)
	}
}

// qualifiedAIBOMDigest reads the pinned digest from the component values file,
// so requalification updates one file rather than one file plus this test.
func qualifiedAIBOMDigest(t *testing.T, provider recipe.DataProvider) string {
	t.Helper()
	raw, err := provider.ReadFile(context.Background(), "components/k8s-aibom/values.yaml")
	if err != nil {
		t.Fatalf("read k8s-aibom values: %v", err)
	}
	var values struct {
		Image struct {
			Digest string `yaml:"digest"`
		} `yaml:"image"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("parse k8s-aibom values: %v", err)
	}
	if values.Image.Digest == "" {
		t.Fatal("k8s-aibom values.yaml has no image.digest")
	}
	return values.Image.Digest
}
