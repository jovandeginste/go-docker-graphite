package main

import (
	"bufio"
	"github.com/fsouza/go-dockerclient"
	"github.com/marpaia/graphite-golang"
	"gopkg.in/alecthomas/kingpin.v2"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

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

var graphite_sender *graphite.Graphite

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))

	var err error

	graphite_sender, err = graphite.NewGraphite(*GraphiteHost, *GraphitePort)
	if err != nil {
		log.Fatal("An error has occurred while trying to create a Graphite connector:", err)
	}

	graphite_sender.Prefix = *GraphitePrefix

	if *Debug {
		log.Printf("Loaded Graphite connection: %#v", graphite_sender)
	}

	if err != nil {
		panic(err)
	}
	client, _ := docker.NewClient("unix:///var/run/docker.sock")
	var name string

	for {
		containers, _ := client.ListContainers(docker.ListContainersOptions{All: false})

		log.Printf("New containers: %d", len(containers))
		for _, c := range containers {
			name = primary_name(c.Names[0])

			if *Debug {
				log.Printf("Container: %s = %s", c.ID, name)
			}
			stats_chan := make(chan *docker.Stats, 1)
			done := make(chan bool)

			go client.Stats(docker.StatsOptions{c.ID, stats_chan, false, done, 0})
			go fetch_stats(stats_chan, name)
		}

		log.Println("Sleeping")
		time.Sleep(time.Duration(*Delay) * time.Millisecond)
	}
}
func primary_name(name string) string {
	return strings.Trim(name, "/")
}

func fetch_stats(stats_chan chan *docker.Stats, container string) {
	var metrics []Metric
	for {
		select {
		case stats, ok := <-stats_chan:
			if !ok {
				return
			}
			metrics = parse_stats(stats)
			go send_container_metrics(container, metrics)
		}
	}
}

func send_container_metrics(n string, metrics []Metric) {
	var metric string
	var m Metric
	for _, m = range metrics {
		metric = *Hostname + "." + n + "." + m.Name
		if *Debug {
			log.Printf("Sending %s=%s", metric, m.Value)
		}
		graphite_sender.SimpleSend(metric, m.Value)
	}
	if *Debug {
		log.Printf("Sent %d metric(s) for %s.%s", len(metrics), *Hostname, n)
	}
}

func parse_stats(stats *docker.Stats) []Metric {
	var result []Metric
	result = append(result, cpumetrics(stats)...)
	result = append(result, memmetrics(stats)...)
	result = append(result, netmetrics(stats)...)
	result = append(result, blkiometrics(stats)...)
	return result
}

func netmetrics(stats *docker.Stats) []Metric {
	var result []Metric
	network := stats.Network
	result = append(result, Metric{"network.rx_dropped", strconv.FormatUint(network.RxDropped, 10)})
	result = append(result, Metric{"network.rx_bytes", strconv.FormatUint(network.RxBytes, 10)})
	result = append(result, Metric{"network.rx_errors", strconv.FormatUint(network.RxErrors, 10)})
	result = append(result, Metric{"network.tx_packets", strconv.FormatUint(network.TxPackets, 10)})
	result = append(result, Metric{"network.tx_dropped", strconv.FormatUint(network.TxDropped, 10)})
	result = append(result, Metric{"network.rx_packets", strconv.FormatUint(network.RxPackets, 10)})
	result = append(result, Metric{"network.tx_errors", strconv.FormatUint(network.TxErrors, 10)})
	result = append(result, Metric{"network.tx_bytes", strconv.FormatUint(network.TxBytes, 10)})
	return result
}

func blkiometrics(stats *docker.Stats) []Metric {
	var result []Metric
	result = append(result, blkiometricspart("blkio_stats.io_service_bytes_recursive", stats.BlkioStats.IOServiceBytesRecursive)...)
	result = append(result, blkiometricspart("blkio_stats.io_serviced_recursive", stats.BlkioStats.IOServicedRecursive)...)
	return result
}

func blkiometricspart(prefix string, blkios []docker.BlkioStatsEntry) []Metric {
	var metrics []Metric
	var dev string
	var name string
	var devA []string
	var typ string
	var value string
	var total uint64
	for _, item := range blkios {
		dev = strconv.FormatUint(item.Major, 10) + ":" + strconv.FormatUint(item.Minor, 10)
		if dev != "" {
			dev = grep("^DEVNAME=", "/sys/dev/block/"+dev+"/uevent")
			if dev != "" {
				devA = strings.SplitN(dev, "=", 2)
				dev = devA[1]
				typ = strings.ToLower(item.Op)
				name = prefix + "." + dev + "." + typ
				value = strconv.FormatUint(item.Value, 10)
				metrics = append(metrics, Metric{name, value})
				if typ == "total" {
					total += item.Value
				}
			}
		}
	}
	name = prefix + ".total"
	value = strconv.FormatUint(total, 10)
	metrics = append(metrics, Metric{name, value})

	return metrics
}

func cpumetrics(stats *docker.Stats) []Metric {
	var result []Metric
	cpu_usage := stats.CPUStats.CPUUsage
	result = append(result, Metric{"cpu_stats.cpu_usage.total_usage", strconv.FormatUint(cpu_usage.TotalUsage, 10)})
	result = append(result, Metric{"cpu_stats.cpu_usage.usage_in_kernelmode", strconv.FormatUint(cpu_usage.UsageInKernelmode, 10)})
	result = append(result, Metric{"cpu_stats.cpu_usage.usage_in_usermode", strconv.FormatUint(cpu_usage.UsageInUsermode, 10)})
	result = append(result, Metric{"cpu_stats.system_cpu_usage", strconv.FormatUint(stats.CPUStats.SystemCPUUsage, 10)})
	return result
}

func memmetrics(stats *docker.Stats) []Metric {
	var result []Metric
	memory_stats := stats.MemoryStats.Stats
	result = append(result, Metric{"memory_stats.failcnt", strconv.FormatUint(stats.MemoryStats.Failcnt, 10)})
	result = append(result, Metric{"memory_stats.limit", strconv.FormatUint(stats.MemoryStats.Limit, 10)})
	result = append(result, Metric{"memory_stats.max_usage", strconv.FormatUint(stats.MemoryStats.MaxUsage, 10)})
	result = append(result, Metric{"memory_stats.stats.active_anon", strconv.FormatUint(memory_stats.ActiveAnon, 10)})
	result = append(result, Metric{"memory_stats.stats.active_file", strconv.FormatUint(memory_stats.ActiveFile, 10)})
	result = append(result, Metric{"memory_stats.stats.cache", strconv.FormatUint(memory_stats.Cache, 10)})
	result = append(result, Metric{"memory_stats.stats.hierarchical_memory_limit", strconv.FormatUint(memory_stats.HierarchicalMemoryLimit, 10)})
	result = append(result, Metric{"memory_stats.stats.inactive_anon", strconv.FormatUint(memory_stats.InactiveAnon, 10)})
	result = append(result, Metric{"memory_stats.stats.inactive_file", strconv.FormatUint(memory_stats.InactiveFile, 10)})
	result = append(result, Metric{"memory_stats.stats.mapped_file", strconv.FormatUint(memory_stats.MappedFile, 10)})
	result = append(result, Metric{"memory_stats.stats.pgfault", strconv.FormatUint(memory_stats.Pgfault, 10)})
	result = append(result, Metric{"memory_stats.stats.pgmajfault", strconv.FormatUint(memory_stats.Pgmajfault, 10)})
	result = append(result, Metric{"memory_stats.stats.pgpgin", strconv.FormatUint(memory_stats.Pgpgin, 10)})
	result = append(result, Metric{"memory_stats.stats.pgpgout", strconv.FormatUint(memory_stats.Pgpgout, 10)})
	result = append(result, Metric{"memory_stats.stats.rss", strconv.FormatUint(memory_stats.Rss, 10)})
	result = append(result, Metric{"memory_stats.stats.rss_huge", strconv.FormatUint(memory_stats.RssHuge, 10)})
	result = append(result, Metric{"memory_stats.stats.total_active_anon", strconv.FormatUint(memory_stats.TotalActiveAnon, 10)})
	result = append(result, Metric{"memory_stats.stats.total_active_file", strconv.FormatUint(memory_stats.TotalActiveFile, 10)})
	result = append(result, Metric{"memory_stats.stats.total_cache", strconv.FormatUint(memory_stats.TotalCache, 10)})
	result = append(result, Metric{"memory_stats.stats.total_inactive_anon", strconv.FormatUint(memory_stats.TotalInactiveAnon, 10)})
	result = append(result, Metric{"memory_stats.stats.total_inactive_file", strconv.FormatUint(memory_stats.TotalInactiveFile, 10)})
	result = append(result, Metric{"memory_stats.stats.total_mapped_file", strconv.FormatUint(memory_stats.TotalMappedFile, 10)})
	result = append(result, Metric{"memory_stats.stats.total_pgfault", strconv.FormatUint(memory_stats.TotalPgfault, 10)})
	result = append(result, Metric{"memory_stats.stats.total_pgmajfault", strconv.FormatUint(memory_stats.TotalPgmafault, 10)})
	result = append(result, Metric{"memory_stats.stats.total_pgpgin", strconv.FormatUint(memory_stats.TotalPgpgin, 10)})
	result = append(result, Metric{"memory_stats.stats.total_pgpgout", strconv.FormatUint(memory_stats.TotalPgpgout, 10)})
	result = append(result, Metric{"memory_stats.stats.total_rss", strconv.FormatUint(memory_stats.TotalRss, 10)})
	result = append(result, Metric{"memory_stats.stats.total_rss_huge", strconv.FormatUint(memory_stats.TotalRssHuge, 10)})
	result = append(result, Metric{"memory_stats.stats.total_unevictable", strconv.FormatUint(memory_stats.TotalUnevictable, 10)})
	result = append(result, Metric{"memory_stats.stats.total_writeback", strconv.FormatUint(memory_stats.TotalWriteback, 10)})
	result = append(result, Metric{"memory_stats.stats.unevictable", strconv.FormatUint(memory_stats.Unevictable, 10)})
	result = append(result, Metric{"memory_stats.stats.writeback", strconv.FormatUint(memory_stats.Writeback, 10)})
	result = append(result, Metric{"memory_stats.usage", strconv.FormatUint(stats.MemoryStats.Usage, 10)})

	return result
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
