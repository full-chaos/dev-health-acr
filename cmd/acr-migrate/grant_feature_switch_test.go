package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The grant Job and the workloads must read each feature switch from the same
// chart value: a Job that saw the feature off while the pods saw it on would
// leave the pods without a privilege they use (or the reverse). Each switch is
// a value expression in two templates; they must be identical.
func TestGrantJobReadsEachFeatureSwitchFromTheSameChartValueAsTheWorkloads(t *testing.T) {
	configmap, err := os.ReadFile("../../deploy/helm/acr/templates/configmap.yaml")
	require.NoError(t, err)
	projectorConfigmap, err := os.ReadFile("../../deploy/helm/acr/templates/projector-configmap.yaml")
	require.NoError(t, err)
	job, err := os.ReadFile("../../deploy/helm/acr/templates/runtime-acl-job.yaml")
	require.NoError(t, err)

	for _, name := range []string{"ACR_ENABLE_EPISODE_WRITEBACK", "ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE", "ACR_WORKLOAD_TRUST_DOMAIN"} {
		inConfigmap := regexp.MustCompile(`(?m)^\s*` + name + `:\s*(\{\{.*?\}\})\s*$`).FindSubmatch(configmap)
		require.NotNil(t, inConfigmap, "%s is not set in configmap.yaml", name)
		inJob := regexp.MustCompile(`(?m)- name: ` + name + `\n\s*value:\s*(\{\{.*?\}\})\s*$`).FindSubmatch(job)
		require.NotNil(t, inJob, "%s is not set in runtime-acl-job.yaml", name)
		require.Equal(t, string(inConfigmap[1]), string(inJob[1]), "%s: the Job and the workloads must read the same chart value", name)
	}
	inProjector := regexp.MustCompile(`(?m)^\s*ACR_ENABLE_EPISODE_WRITEBACK:\s*(\{\{.*?\}\})\s*$`).FindSubmatch(projectorConfigmap)
	require.NotNil(t, inProjector)
	inJob := regexp.MustCompile(`(?m)- name: ACR_ENABLE_EPISODE_WRITEBACK\n\s*value:\s*(\{\{.*?\}\})\s*$`).FindSubmatch(job)
	require.Equal(t, string(inProjector[1]), string(inJob[1]), "the projector reads the episode switch from the same value as the Job")

	// The workloads only see the workload settings when the feature is enabled,
	// so the Job must be gated on the same condition.
	gate := regexp.MustCompile(`(?s)\{\{-\s*if \.Values\.workloadTokenExchange\.enabled\s*\}\}\s*- name: ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE.*?- name: ACR_WORKLOAD_TRUST_DOMAIN.*?\{\{-\s*end\s*\}\}`)
	require.Regexp(t, gate, string(job), "the Job passes the workload settings only when workloadTokenExchange.enabled, like the ConfigMap")
	require.Regexp(t, `(?s)\{\{-\s*if \.Values\.workloadTokenExchange\.enabled\s*\}\}\s*ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE`, string(configmap), "the ConfigMap gate this test mirrors")
}
