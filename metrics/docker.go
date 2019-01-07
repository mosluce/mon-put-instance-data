package metrics

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	. "github.com/poddworks/mon-put-instance-data/services"
	"github.com/shirou/gopsutil/docker"
)

// Docker metric entity
type Docker struct{}

// Docker CPU Usage metric history
var usage_history map[string]float64

// Docker Time history
var last_time_at time.Time

const (
	nanoseconds = 1e9

	maxMetricDataNum = 20
)

// On older systems, the control groups might be mounted on /cgroup
func getCgroupMountPath() (string, error) {
	out, err := exec.Command("grep", "-m1", "cgroup", "/proc/mounts").Output()
	if err != nil {
		return "", errors.New("Cannot figure out where control groups are mounted")
	}
	res := strings.Fields(string(out))
	if strings.HasPrefix(res[1], "/cgroup") {
		return "/cgroup", nil
	}
	return "/sys/fs/cgroup", nil
}

// Collect CPU & Memory usage per Docker Container
func (d Docker) Collect(instanceID string, c CloudWatchService, namespace string) {
	containers, err := docker.GetDockerStat()
	if err != nil {
		log.Fatal(err)
	}

	base, err := getCgroupMountPath()
	if err != nil {
		log.Fatal(err)
	}

	new_usage_history, new_last_time_at := make(map[string]float64), time.Now()

	var metricArr []cloudwatch.MetricDatum

	for _, container := range containers {
		dimensions := make([]cloudwatch.Dimension, 0)
		dimensionKey1 := "InstanceId"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey1,
			Value: &instanceID,
		})
		dimensionKey2 := "ContainerId"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey2,
			Value: aws.String(container.ContainerID),
		})
		dimensionKey3 := "ContainerName"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey3,
			Value: aws.String(container.Name),
		})
		dimensionKey4 := "DockerImage"
		dimensions = append(dimensions, cloudwatch.Dimension{
			Name:  &dimensionKey4,
			Value: aws.String(container.Image),
		})

		containerMemory, err := docker.CgroupMem(container.ContainerID, fmt.Sprintf("%s/memory/docker", base))
		if err != nil {
			log.Fatal(err)
		}

		containerMemoryData := constructMetricDatum("ContainerMemory", float64(containerMemory.MemUsageInBytes), cloudwatch.StandardUnitBytes, dimensions)
		metricArr = append(metricArr, containerMemoryData...)

		containerCPU, err := docker.CgroupCPU(container.ContainerID, fmt.Sprintf("%s/cpuacct/docker", base))
		if err != nil {
			log.Fatal(err)
		}

		containerCPUUserData := constructMetricDatum("ContainerCPUUser", float64(containerCPU.User), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, containerCPUUserData...)

		containerCPUSystemData := constructMetricDatum("ContainerCPUSystem", float64(containerCPU.System), cloudwatch.StandardUnitSeconds, dimensions)
		metricArr = append(metricArr, containerCPUSystemData...)

		var percentCPU float64 = -1

		containerCPUUsage, err := docker.CgroupCPUUsageDocker(container.ContainerID)
		if err != nil {
			log.Fatal(err)
		} else {
			new_usage_history[container.ContainerID] = containerCPUUsage
			if pastContainerCPUUsage, okay := usage_history[container.ContainerID]; okay {
				percentCPU = (containerCPUUsage - pastContainerCPUUsage) / float64(new_last_time_at.Sub(last_time_at)) * 100 * nanoseconds
				containerCPUUsageData := constructMetricDatum("ContainerCPUUsage", percentCPU, cloudwatch.StandardUnitPercent, dimensions)
				metricArr = append(metricArr, containerCPUUsageData...)
			}
		}

		log.Printf("Docker - Container:%s Memory:%v User:%v System:%v Percent:%v\n", container.Name, containerMemory.MemMaxUsageInBytes, containerCPU.User, containerCPU.System, percentCPU)
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

	// Swap data, continue from here
	usage_history, last_time_at = new_usage_history, time.Now()
}

func init() {
	// initializae usage_history mapping
	usage_history = make(map[string]float64)

	// record timestamp
	last_time_at = time.Now()
}
