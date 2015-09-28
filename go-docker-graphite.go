package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/marpaia/graphite-golang"
	"gopkg.in/alecthomas/kingpin.v2"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

type Container struct {
	Command string
	Created int
	Id      string
	Image   string
	Names   []string
	Ports   []string
	Status  string
}

type Metric struct {
	Name  string
	Value string
}

var (
	app            = kingpin.New("go-docker-graphite", "A tool to report container metrics to a graphite backend")
	Debug          = app.Flag("debug", "Enable verbose logging").Bool()
	Hostname       = app.Flag("hostname", "hostname to report").Default("me").String()
	GraphiteHost   = app.Flag("host", "graphite host").Required().String()
	GraphitePort   = app.Flag("port", "graphite port").Default("2003").Int()
	GraphitePrefix = app.Flag("prefix", "graphite prefix").Default("containers.metrics").String()
	Delay          = app.Flag("delay", "delay between metric reports").Default("10000").Int()
)

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))

	graphite, err := graphite.NewGraphite(*GraphiteHost, *GraphitePort)
	if err != nil {
		log.Fatal("An error has occurred while trying to create a Graphite connector:", err)
	}

	graphite.Prefix = *GraphitePrefix

	if *Debug {
		log.Printf("Loaded Graphite connection: %#v", graphite)
	}

	if err != nil {
		panic(err)
	}

	for {
		containers, _ := get_containers()
		for _, c := range containers {
			if *Debug {
				log.Printf("Container: %s = %s", c.Id, c.PrimaryName())
			}
			send_container_metrics(*Hostname, c, graphite)
		}
		time.Sleep(time.Duration(*Delay) * time.Millisecond)
	}
}

func send_container_metrics(h string, c Container, graphite *graphite.Graphite) {
	n := c.PrimaryName()
	var metric string
	var i int
	var m Metric
	for i, m = range c.Metrics() {
		metric = h + "." + n + "." + m.Name
		graphite.SimpleSend(metric, m.Value)
	}
	if *Debug {
		log.Printf("Sent %d metrics for %s.%s", i, h, n)
	}
}

func get_containers() ([]Container, error) {
	c, err := net.Dial("unix", "/var/run/docker.sock")
	if err != nil {
		return nil, err
	}

	if *Debug {
		log.Println("Sending request...")
	}
	_, err = c.Write([]byte("GET /containers/json HTTP/1.0\r\n\r\n"))
	if err != nil {
		return nil, err
	}

	var result []byte

	var in_bytes = make([]byte, 102400)
	for {
		num, err := c.Read(in_bytes)
		result = append(result, in_bytes...)
		if err != nil || num < len(in_bytes) {
			break
		}
	}
	result = bytes.Trim(result, "\x00")
	results := bytes.SplitN(result, []byte{'\r', '\n', '\r', '\n'}, 2)
	jsonBlob := results[1]
	if *Debug {
		log.Println("Got response:")
		log.Println(string(jsonBlob))
	}

	var containers []Container
	err = json.Unmarshal(jsonBlob, &containers)
	return containers, err
}

func key_value_to_metric(prefix string, data string) []Metric {
	var metrics []Metric
	var split []string
	var name string
	var value string
	for _, line := range strings.Split(data, "\n") {
		split = strings.SplitN(line, " ", 2)
		name = split[0]
		if name != "" {
			name = prefix + "." + name
			value = split[1]
			metrics = append(metrics, Metric{name, value})
		}
	}

	return metrics
}

func (c Container) PrimaryName() string {
	primary_name := c.Names[0]
	if primary_name == "" {
		return ""
	}
	primary_name = strings.Trim(primary_name, "/")
	return primary_name
}

func (c Container) cpuacctFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/cpu,cpuacct/system.slice/docker-%s.scope/cpuacct.stat", c.Id)
}

func (c Container) memoryFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/memory/system.slice/docker-%s.scope/memory.stat", c.Id)
}

func (c Container) cpuacctMetrics() []Metric {
	data, err := ioutil.ReadFile(c.cpuacctFile())
	if err != nil {
		return nil
	}
	return key_value_to_metric("cpu", string(data))
}

func (c Container) memoryMetrics() []Metric {
	data, err := ioutil.ReadFile(c.memoryFile())
	if err != nil {
		return nil
	}
	return key_value_to_metric("memory", string(data))
}

func (c Container) Metrics() []Metric {
	var metrics []Metric
	metrics = append(metrics, c.cpuacctMetrics()...)
	metrics = append(metrics, c.memoryMetrics()...)
	return metrics
}
