// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package stack

import (
	"fmt"
	"strings"

	"github.com/elastic/elastic-package/internal/compose"
	"github.com/elastic/elastic-package/internal/docker"
	"github.com/elastic/elastic-package/internal/install"
)

type ServiceStatus struct {
	Name    string
	Status  string
	Version string
}

const readyServicesSuffix = "is_ready"

const (
	// serviceLabelDockerCompose is the label with the service name created by docker-compose
	serviceLabelDockerCompose = "com.docker.compose.service"
	// projectLabelDockerCompose is the label with the project name created by docker-compose
	projectLabelDockerCompose = "com.docker.compose.project"
)

type envBuilder struct {
	vars []string
}

// TODO: Use template variables instead of environment variables to parameterize docker-compose.
func newEnvBuilder() *envBuilder {
	return new(envBuilder)
}

func (eb *envBuilder) withEnvs(envs []string) *envBuilder {
	eb.vars = append(eb.vars, envs...)
	return eb
}

func (eb *envBuilder) withEnv(env string) *envBuilder {
	eb.vars = append(eb.vars, env)
	return eb
}

func (eb *envBuilder) build() []string {
	return eb.vars
}

func dockerComposeBuild(options Options) error {
	c, err := compose.NewProject(DockerComposeProjectName(options.Profile), options.Profile.Path(profileStackPath, SnapshotFile))
	if err != nil {
		return fmt.Errorf("could not create docker compose project: %w", err)
	}

	appConfig, err := install.Configuration()
	if err != nil {
		return fmt.Errorf("can't read application configuration: %w", err)
	}

	opts := compose.CommandOptions{
		Env: newEnvBuilder().
			withEnvs(appConfig.StackImageRefs(options.StackVersion).AsEnv()).
			withEnv(stackVariantAsEnv(options.StackVersion)).
			withEnvs(options.Profile.ComposeEnvVars()).
			build(),
		Services: withIsReadyServices(withDependentServices(options.Services)),
	}

	if err := c.Build(opts); err != nil {
		return fmt.Errorf("running command failed: %w", err)
	}
	return nil
}

func dockerComposePull(options Options) error {
	c, err := compose.NewProject(DockerComposeProjectName(options.Profile), options.Profile.Path(profileStackPath, SnapshotFile))
	if err != nil {
		return fmt.Errorf("could not create docker compose project: %w", err)
	}

	appConfig, err := install.Configuration()
	if err != nil {
		return fmt.Errorf("can't read application configuration: %w", err)
	}

	opts := compose.CommandOptions{
		Env: newEnvBuilder().
			withEnvs(appConfig.StackImageRefs(options.StackVersion).AsEnv()).
			withEnv(stackVariantAsEnv(options.StackVersion)).
			withEnvs(options.Profile.ComposeEnvVars()).
			build(),
		Services: withIsReadyServices(withDependentServices(options.Services)),
	}

	if err := c.Pull(opts); err != nil {
		return fmt.Errorf("running command failed: %w", err)
	}
	return nil
}

func dockerComposeUp(options Options) error {
	c, err := compose.NewProject(DockerComposeProjectName(options.Profile), options.Profile.Path(profileStackPath, SnapshotFile))
	if err != nil {
		return fmt.Errorf("could not create docker compose project: %w", err)
	}

	var args []string
	if options.DaemonMode {
		args = append(args, "-d")
	}

	appConfig, err := install.Configuration()
	if err != nil {
		return fmt.Errorf("can't read application configuration: %w", err)
	}

	opts := compose.CommandOptions{
		Env: newEnvBuilder().
			withEnvs(appConfig.StackImageRefs(options.StackVersion).AsEnv()).
			withEnv(stackVariantAsEnv(options.StackVersion)).
			withEnvs(options.Profile.ComposeEnvVars()).
			build(),
		ExtraArgs: args,
		Services:  withIsReadyServices(withDependentServices(options.Services)),
	}

	if err := c.Up(opts); err != nil {
		return fmt.Errorf("running command failed: %w", err)
	}
	return nil
}

func dockerComposeDown(options Options) error {
	c, err := compose.NewProject(DockerComposeProjectName(options.Profile), options.Profile.Path(profileStackPath, SnapshotFile))
	if err != nil {
		return fmt.Errorf("could not create docker compose project: %w", err)
	}

	appConfig, err := install.Configuration()
	if err != nil {
		return fmt.Errorf("can't read application configuration: %w", err)
	}

	downOptions := compose.CommandOptions{
		Env: newEnvBuilder().
			withEnvs(appConfig.StackImageRefs(options.StackVersion).AsEnv()).
			withEnv(stackVariantAsEnv(options.StackVersion)).
			withEnvs(options.Profile.ComposeEnvVars()).
			build(),
		// Remove associated volumes.
		ExtraArgs: []string{"--volumes", "--remove-orphans"},
	}
	if err := c.Down(downOptions); err != nil {
		return fmt.Errorf("running command failed: %w", err)
	}
	return nil
}

func withDependentServices(services []string) []string {
	for _, aService := range services {
		if aService == "elastic-agent" {
			return []string{} // elastic-agent service requires to load all other services
		}
	}
	return services
}

func withIsReadyServices(services []string) []string {
	if len(services) == 0 {
		return services // load all defined services
	}

	var allServices []string
	for _, aService := range services {
		allServices = append(allServices, aService, fmt.Sprintf("%s_%s", aService, readyServicesSuffix))
	}
	return allServices
}

func dockerComposeStatus(options Options) ([]ServiceStatus, error) {
	var services []ServiceStatus
	// query directly to docker to avoid load environment variables (e.g. STACK_VERSION_VARIANT) and profiles
	containerIDs, err := docker.ContainerIDsWithLabel(projectLabelDockerCompose, DockerComposeProjectName(options.Profile))
	if err != nil {
		return nil, err
	}

	if len(containerIDs) == 0 {
		return services, nil
	}

	containerDescriptions, err := docker.InspectContainers(containerIDs...)
	if err != nil {
		return nil, err
	}

	for _, containerDescription := range containerDescriptions {
		service, err := newServiceStatus(&containerDescription)
		if err != nil {
			return nil, err
		}
		services = append(services, *service)
	}

	return services, nil
}

func newServiceStatus(description *docker.ContainerDescription) (*ServiceStatus, error) {
	service := ServiceStatus{
		Name:    description.Config.Labels[serviceLabelDockerCompose],
		Status:  description.State.Status,
		Version: getVersionFromDockerImage(description.Config.Image),
	}
	if description.State.Status == "running" {
		healthStatus := "unknown health"
		if health := description.State.Health; health != nil {
			healthStatus = health.Status
		}
		service.Status = fmt.Sprintf("%v (%v)", service.Status, healthStatus)
	}
	if description.State.Status == "exited" {
		service.Status = fmt.Sprintf("%v (%v)", service.Status, description.State.ExitCode)
	}

	return &service, nil
}

func getVersionFromDockerImage(dockerImage string) string {
	_, version, found := strings.Cut(dockerImage, ":")
	if found {
		return version
	}
	return "latest"
}
