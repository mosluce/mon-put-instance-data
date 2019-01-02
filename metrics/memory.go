package metrics

import (
	"log"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	. "github.com/poddworks/mon-put-instance-data/services"
	"github.com/shirou/gopsutil/mem"
)

// Memory metric entity
type Memory struct{}

// Collect Memory utilization
func (m Memory) Collect(instanceID string, c CloudWatchService, namespace string) {
	memoryMetrics, err := mem.VirtualMemory()
	if err != nil {
		log.Fatal(err)
	}

	dimensionKey := "InstanceId"
	dimensions := []cloudwatch.Dimension{
		cloudwatch.Dimension{
			Name:  &dimensionKey,
			Value: &instanceID,
		},
	}

	memoryUtilizationData := constructMetricDatum("MemoryUtilization", memoryMetrics.UsedPercent, cloudwatch.StandardUnitPercent, dimensions)
	c.Publish(memoryUtilizationData, namespace)

	memoryUsedData := constructMetricDatum("MemoryUsed", float64(memoryMetrics.Used), cloudwatch.StandardUnitBytes, dimensions)
	c.Publish(memoryUsedData, namespace)

	memoryAvailableData := constructMetricDatum("MemoryAvailable", float64(memoryMetrics.Available), cloudwatch.StandardUnitBytes, dimensions)
	c.Publish(memoryAvailableData, namespace)

	log.Printf("Memory - Utilization:%v%% Used:%v Available:%v\n", memoryMetrics.UsedPercent, memoryMetrics.Used, memoryMetrics.Available)
}
