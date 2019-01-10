package metrics

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"regexp"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	. "github.com/poddworks/mon-put-instance-data/services"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Swarm metric entity
type Swarm struct{}

type ServicesStats struct {
	// CPU Usage
	TotalUsage     uint64 `json:"total_usage"`
	SystemUsage    uint64 `json:"system_cpu_usage,omitempty"`
	PreTotalUsage  uint64 `json:"pre_total_usage"`
	PreSystemUsage uint64 `json:"pre_system_cpu_usage,omitempty"`
	OnlineCPUs     uint32 `json:"online_cpus,omitempty"`

	// Memory Usage
	MemoryUsage    uint64 `json:"memory_usage,omitempty"`
	MemoryMaxUsage uint64 `json:"memory_max_usage,omitempty"`
}

const ()

var (
	listOpts types.ContainerListOptions

	dockerCli *client.Client

	re = regexp.MustCompile("-[[:digit:]]+|_[[:alnum:]]{4}_[[:digit:]]+_[[:digit:]]+_[[:digit:]]+$$")
)

// Collect CPU & Memory usage per Swarm Services on this instance
func (s Swarm) Collect(instanceID string, c CloudWatchService, namespace string) {
	containers, err := dockerCli.ContainerList(context.Background(), listOpts)
	if err != nil {
		log.Fatal(err)
	}

	var (
		metricArr []cloudwatch.MetricDatum

		swarm = make(map[string]ServicesStats)

		statsCh = make(chan *types.StatsJSON, len(containers))

		wg sync.WaitGroup
	)

	wg.Add(len(containers))

	// Setup producers
	for _, container := range containers {
		go collectStats(&wg, statsCh, container)
	}

	// Setup sync channel listener
	go collectHalt(&wg, statsCh)

	for v := range statsCh {
		if entry, ok := swarm[v.Name]; !ok {
			swarm[v.Name] = ServicesStats{
				TotalUsage:     v.CPUStats.CPUUsage.TotalUsage,
				SystemUsage:    v.CPUStats.SystemUsage,
				PreTotalUsage:  v.PreCPUStats.CPUUsage.TotalUsage,
				PreSystemUsage: v.PreCPUStats.SystemUsage,
				OnlineCPUs:     v.CPUStats.OnlineCPUs,
				MemoryUsage:    v.MemoryStats.Usage,
				MemoryMaxUsage: v.MemoryStats.MaxUsage,
			}
		} else {
			entry.TotalUsage += v.CPUStats.CPUUsage.TotalUsage
			entry.SystemUsage += v.CPUStats.SystemUsage
			entry.PreTotalUsage += v.PreCPUStats.CPUUsage.TotalUsage
			entry.PreSystemUsage += v.PreCPUStats.SystemUsage
			entry.MemoryUsage += v.MemoryStats.Usage
			entry.MemoryMaxUsage += v.MemoryStats.MaxUsage
		}
	}

	for name, stats := range swarm {
		percentCPU := float64(stats.TotalUsage-stats.PreTotalUsage) / float64(stats.SystemUsage-stats.PreSystemUsage) * float64(stats.OnlineCPUs) * 100

		log.Printf("%s - PercentCPU:%v TotalUsage:%v PreTotalUsage:%v SystemUsage:%v PreSystemUsage:%v MemoryUsage:%v MemoryMaxUsage:%v OnlineCPUs:%v\n",
			name, percentCPU,
			stats.TotalUsage, stats.PreTotalUsage,
			stats.SystemUsage, stats.PreSystemUsage,
			stats.MemoryUsage, stats.MemoryMaxUsage,
			stats.OnlineCPUs)

		dimensions := make([]cloudwatch.Dimension, 0)
		dimensionKey1 := "InstanceId"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey1,
			Value: &instanceID,
		})
		dimensionKey2 := "ServiceName"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey2,
			Value: aws.String(name),
		})

		serviceCPUPercentData := constructMetricDatum("ServiceCPUPercent", percentCPU, cloudwatch.StandardUnitPercent, dimensions)
		metricArr = append(metricArr, serviceCPUPercentData...)

		serviceCPUUserData := constructMetricDatum("ServiceCPUUsage", float64(stats.TotalUsage), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, serviceCPUUserData...)

		serviceCPUSystemData := constructMetricDatum("ServiceCPUSystem", float64(stats.SystemUsage), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, serviceCPUSystemData...)

		servicePreCPUUserData := constructMetricDatum("ServicePreCPUUsage", float64(stats.PreTotalUsage), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, servicePreCPUUserData...)

		servicePreCPUSystemData := constructMetricDatum("ServicePreCPUSystem", float64(stats.PreSystemUsage), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, servicePreCPUSystemData...)

		serviceOnlineCPUsData := constructMetricDatum("ServiceOnlineCPUs", float64(stats.OnlineCPUs), cloudwatch.StandardUnitCount, dimensions)
		metricArr = append(metricArr, serviceOnlineCPUsData...)

		serviceMemoryUsageData := constructMetricDatum("ServiceMemoryUsage", float64(stats.MemoryUsage), cloudwatch.StandardUnitBytes, dimensions)
		metricArr = append(metricArr, serviceMemoryUsageData...)

		serviceMemoryMaxUsageData := constructMetricDatum("ServiceMemoryMaxUsage", float64(stats.MemoryMaxUsage), cloudwatch.StandardUnitBytes, dimensions)
		metricArr = append(metricArr, serviceMemoryMaxUsageData...)
	}

	// Dispatch collected metric data
	for idx := 0; idx < len(metricArr); idx += maxMetricDataNum {
		var slice []cloudwatch.MetricDatum
		if idx+maxMetricDataNum >= len(metricArr) {
			slice = metricArr[idx:]
		} else {
			slice = metricArr[idx : idx+maxMetricDataNum]
		}
		c.Publish(slice, namespace)
	}
}

func collectStats(wg *sync.WaitGroup, ch chan<- *types.StatsJSON, c types.Container) {
	var (
		name = c.Labels["com.docker.swarm.service.name"]

		v *types.StatsJSON
	)

	defer wg.Done() // mark done upon go rouine end

	name = re.ReplaceAllString(name, "")

	response, err := dockerCli.ContainerStats(context.Background(), c.ID, false)
	if err != nil {
		log.Fatal(err)
	} else {
		defer response.Body.Close()
		content, err := ioutil.ReadAll(response.Body)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(content, &v); err != nil {
			log.Fatal(err)
		}
	}

	v.Name = name // override name with swarm service name

	ch <- v // report container stats
}

func collectHalt(wg *sync.WaitGroup, ch chan<- *types.StatsJSON) {
	wg.Wait()
	close(ch)
}

func init() {
	var err error

	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.swarm.service.name")

	listOpts.Filters = filterArgs

	dockerCli, err = client.NewEnvClient()
	if err != nil {
		panic(err)
	}
}
