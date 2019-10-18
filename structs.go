package main

type Container struct {
	Command string
	Created int
	Id      string
	Image   string
	Name    string
	Ports   []ContainerPort
	Status  string
	Config  ContainerConfig
}

type ContainerPort struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

type ContainerConfig struct {
	Env []string
}

type Metric struct {
	Name  string
	Value string
}
