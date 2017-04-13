package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/marpaia/graphite-golang"
	"github.com/vishvananda/netns"
	"gopkg.in/alecthomas/kingpin.v2"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

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

var (
	app            = kingpin.New("go-docker-graphite", "A tool to report container metrics to a graphite backend")
	Debug          = app.Flag("debug", "Enable verbose logging").Bool()
	Hostname       = app.Flag("hostname", "hostname to report").Default("me").String()
	GraphiteHost   = app.Flag("host", "graphite host").Required().String()
	GraphitePort   = app.Flag("port", "graphite port").Default("2003").Int()
	GraphitePrefix = app.Flag("prefix", "graphite prefix").Default("containers.metrics").String()
	Delay          = app.Flag("delay", "delay between metric reports").Default("10000").Int()
	DockerHost     = app.Flag("dockerhost", "Docker host to contact").Default("unix:/var/run/docker.sock").String()
)

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))

	split := strings.SplitN(*DockerHost, ":", 2)
	proto := split[0]
	conn := split[1]

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
		containers, err := get_containers(proto, conn)
		if err != nil {
			log.Printf("An error occurred: %s", err)
		} else {
			for _, c := range containers {
				_ = c.GetInfo(proto, conn)
				send_container_metrics(*Hostname, c, graphite)
			}
		}
		time.Sleep(time.Duration(*Delay) * time.Millisecond)
	}
}

func (c *Container) GetInfo(proto string, conn string) (err error) {
	netconn, err := net.Dial(proto, conn)
	if err != nil {
		return err
	}

	id := c.Id
	url := "/containers/" + id + "/json"

	if *Debug {
		log.Println("Sending request..." + url)
	}
	_, err = netconn.Write([]byte("GET " + url + " HTTP/1.0\r\n\r\n"))
	if err != nil {
		return err
	}

	var result []byte

	var in_bytes = make([]byte, 102400)
	for {
		num, err := netconn.Read(in_bytes)
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

	var container Container
	err = json.Unmarshal(jsonBlob, &container)
	*c = container
	return err
}

func send_container_metrics(h string, c Container, graphite *graphite.Graphite) {
	n, err := c.PrimaryName(h)
	if err != nil {
		log.Printf("An error occurred: %s", err)
		return
	}
	if *Debug {
		log.Printf("Container: %s = %s", c.Id, n)
	}
	var metric string
	var m Metric
	metrics := c.Metrics()
	for _, m = range metrics {
		metric = n + "." + m.Name
		graphite.SimpleSend(metric, m.Value)
	}
	if *Debug {
		log.Printf("Sent %d metrics for %s.%s", len(metrics), h, n)
	}
}

func get_containers(proto string, conn string) ([]Container, error) {
	c, err := net.Dial(proto, conn)
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

func (c Container) PrimaryName(hostname string) (string, error) {
	name := ""
	if name == "" {
		alloc_name := find_value(c.Config.Env, "NOMAD_ALLOC_NAME")
		if len(alloc_name) > 0 {
			job_name := find_value(c.Config.Env, "NOMAD_JOB_NAME")
			task_name := find_value(c.Config.Env, "NOMAD_TASK_NAME")
			task_name = strings.TrimPrefix(task_name, job_name)
			task_name = strings.TrimPrefix(task_name, "-")
			if len(task_name) == 0 {
				task_name = "default"
			}
			name = "nomad." + alloc_name + "." + task_name
		}
	}
	if name == "" {
		name = find_value(c.Config.Env, "SERVICE_NAME")
		if len(name) > 0 {
			tag := "default"
			tags := find_value(c.Config.Env, "SERVICE_TAGS")
			if len(tags) > 0 {
				split_tags := strings.SplitN(tags, ",", 2)
				tag = split_tags[0]
			}
			name = "registrator." + name + "." + tag + "." + hostname
		}
	}
	if name == "" {
		name = c.Name
		if len(name) > 0 {
			stripUuid, _ := regexp.Compile("-[0-9a-z]{8}-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{12}")
			name = stripUuid.ReplaceAllString(name, "")
			name = "random." + name + ".main." + hostname
		}
	}
	if name == "" {
		return "", fmt.Errorf("Could not find a sane name for container '%s'", c.Id)
	}

	stripNonWord, _ := regexp.Compile("[^A-Za-z0-9_\\.\\-]+")
	name = stripNonWord.ReplaceAllString(name, "_")

	removeDoubleIllegalChars, _ := regexp.Compile("__+")
	name = removeDoubleIllegalChars.ReplaceAllString(name, "_")

	trimIllegalChars, _ := regexp.Compile("_?\\._?")
	name = trimIllegalChars.ReplaceAllString(name, ".")

	return name, nil
}

func (c Container) cpuacctFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/cpu,cpuacct/system.slice/docker-%s.scope/cpuacct.stat", c.Id)
}

func (c Container) memoryFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/memory/system.slice/docker-%s.scope/memory.stat", c.Id)
}

func (c Container) blkioFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/blkio/system.slice/docker-%s.scope/blkio.throttle.io_service_bytes", c.Id)
}

func (c Container) tasksFile() string {
	return fmt.Sprintf("/sys/fs/cgroup/memory/system.slice/docker-%s.scope/tasks", c.Id)
}

func (c Container) firstPid() (int, error) {
	data, err := ioutil.ReadFile(c.tasksFile())
	if err != nil {
		return -1, err
	}
	value, err := strconv.Atoi(strings.SplitN(string(data), "\n", 2)[0])
	if err != nil {
		return -1, err
	}
	return value, nil
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

func (c Container) blkioMetrics() []Metric {
	data, err := ioutil.ReadFile(c.blkioFile())
	if err != nil {
		return nil
	}
	prefix := "blkio"

	var metrics []Metric
	var split []string
	var name string
	var dev string
	var devA []string
	var typ string
	var value string
	for _, line := range strings.Split(string(data), "\n") {
		split = strings.SplitN(line, " ", 3)
		dev = split[0]
		if dev != "" {
			if dev == "Total" {
				name = prefix + "." + dev
				value = split[1]
				metrics = append(metrics, Metric{name, value})
			} else {
				dev = grep("^DEVNAME=", "/sys/dev/block/"+dev+"/uevent")
				if dev != "" {
					devA = strings.SplitN(dev, "=", 2)
					dev = devA[1]
					typ = split[1]
					name = prefix + "." + dev + "." + typ
					value = split[2]
					metrics = append(metrics, Metric{name, value})
				}
			}
		}
	}

	return metrics
}

func (c Container) Metrics() []Metric {
	var metrics []Metric
	metrics = append(metrics, c.cpuacctMetrics()...)
	metrics = append(metrics, c.memoryMetrics()...)
	metrics = append(metrics, c.blkioMetrics()...)
	metrics = append(metrics, c.netMetrics()...)
	if *Debug {
		log.Printf("Metrics: %s", metrics)
	}
	return metrics
}

func grep(re, filename string) string {
	regex, err := regexp.Compile(re)
	if err != nil {
		return "" // there was a problem with the regular expression.
	}

	fh, err := os.Open(filename)
	f := bufio.NewReader(fh)

	if err != nil {
		return "" // there was a problem opening the file.
	}
	defer fh.Close()

	buf := make([]byte, 1024)
	for {
		buf, _, err = f.ReadLine()
		if err != nil {
			return ""
		}

		s := string(buf)
		if regex.MatchString(s) {
			return string(buf)
		}
	}
}

func (c Container) netMetrics() []Metric {
	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save the current network namespace
	origns, _ := netns.Get()
	defer origns.Close()

	// Create a new network namespace
	pid, _ := c.firstPid()

	newns, _ := netns.GetFromPid(pid)
	defer newns.Close()

	// Switch to the container namespace
	netns.Set(newns)

	data, _ := exec.Command("ip", "-s", "-o", "link").Output()

	// Switch back to the original namespace
	netns.Set(origns)
	runtime.UnlockOSThread()

	int_re, _ := regexp.Compile("^\\d+: ([^:@]+)[:@].*$")
	prefix := "network"

	var metrics []Metric
	var interface_name string
	var name string
	var rx []string
	var tx []string

	for _, link_info := range strings.Split(string(data), "\n") {
		split_link_info := strings.Split(link_info, "\\")
		if len(split_link_info) >= 6 {
			interface_name = int_re.FindStringSubmatch(split_link_info[0])[1]
			name = prefix + "." + interface_name
			rx = strings.Fields(split_link_info[3])
			tx = strings.Fields(split_link_info[5])

			if len(rx) == 6 {
				rx_packets, _ := strconv.Atoi(rx[1])
				if rx_packets > 0 {
					metrics = append(metrics, Metric{name + ".rx.bytes", rx[0]})
					metrics = append(metrics, Metric{name + ".rx.packets", rx[1]})
					metrics = append(metrics, Metric{name + ".rx.errors", rx[2]})
					metrics = append(metrics, Metric{name + ".rx.dropped", rx[3]})
					metrics = append(metrics, Metric{name + ".rx.overrun", rx[4]})
					metrics = append(metrics, Metric{name + ".rx.mcast", rx[5]})
				}
			}

			if len(tx) == 6 {
				tx_packets, _ := strconv.Atoi(tx[1])
				if tx_packets > 0 {
					metrics = append(metrics, Metric{name + ".tx.bytes", tx[0]})
					metrics = append(metrics, Metric{name + ".tx.packets", tx[1]})
					metrics = append(metrics, Metric{name + ".tx.errors", tx[2]})
					metrics = append(metrics, Metric{name + ".tx.dropped", tx[3]})
					metrics = append(metrics, Metric{name + ".tx.overrun", tx[4]})
					metrics = append(metrics, Metric{name + ".tx.mcast", tx[5]})
				}
			}
		}
	}
	return metrics
}

func find_value(ss []string, prefix string) (ret string) {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			split := strings.SplitN(s, "=", 2)
			value := split[1]
			return value
		}
	}
	return ""
}
