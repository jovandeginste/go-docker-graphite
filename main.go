package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/marpaia/graphite-golang"
	"gopkg.in/alecthomas/kingpin.v2"
)

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
			err = graphite.Connect()
			if err != nil {
				log.Printf("Could not connect graphie: %s", err)
				panic(err)
			}
			for _, c := range containers {
				_ = c.GetInfo(proto, conn)
				send_container_metrics(*Hostname, c, graphite)
			}
		}
		time.Sleep(time.Duration(*Delay) * time.Millisecond)
	}
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
		metric = n + "." + m.CleanName()
		graphite.SimpleSend(metric, m.Value)
	}
	if *Debug {
		log.Printf("Sent %d metrics for %s.%s", len(metrics), h, n)
	}
}

func (m *Metric) CleanName() string {
	return strings.TrimSpace(m.Name)
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
		result = append(result, in_bytes[0:num]...)
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
