package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
)


func main() {
	fmt.Println(statusAsJson())
}


func statusAsJson() string {
	
	const jsonVersion = "0.1"
	const memThreshold = 90
	const diskThreshold = 80

	percentMemUsed := percentMemUsed()
	percentDiskUsed := percentDiskUsed()

	returnString := "{\"title\": \"vitals-glimpse\",\"version\":" + jsonVersion + ","

	if percentMemUsed < memThreshold {
		returnString = returnString + fmt.Sprintf("\"mem_status\": \"mem_okay\", \"mem_percent\":")
	} else {
		returnString = returnString + fmt.Sprintf("\"mem_status\": \"mem_fail\", \"mem_percent\":")
	}
	returnString = returnString + strconv.Itoa(percentMemUsed) + ","

	if percentDiskUsed < diskThreshold {
		returnString = returnString + fmt.Sprintf("\"disk_status\": \"disk_okay\", \"disk_percent\":")
	} else {
		returnString = returnString + fmt.Sprintf("\"disk_status\": \"disk_fail\", \"disk_percent\":")
	}
	returnString = returnString + strconv.Itoa(percentDiskUsed) + "}"

	return returnString
}


func parseInt(s string) int {
	var value int
	fmt.Sscanf(s, "%d", &value)
	return value
}


func percentMemUsed() int {
	memInfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		log.Println("Error reading /proc/meminfo:", err)
		return -1
	}

	memInfoLines := strings.Split(string(memInfo), "\n")
	memStats := make(map[string]int)

	for _, line := range memInfoLines {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				key := parts[0]
				value := parts[1]
				memStats[key] = parseInt(value)
			}
	}

	percentAvail := float32(memStats["MemAvailable:"])*100/(float32(memStats["MemTotal:"]))
	return 100-int(percentAvail)
}


func percentDiskUsed() int {
	var stat syscall.Statfs_t

	err := syscall.Statfs("/", &stat)
	if err != nil {
		log.Println("Error fetching Statfs for '/'", err)
		return -1
	}

	totalBlocks := stat.Blocks
	availableBlocks := stat.Bavail

	var totalSpace = int(totalBlocks)
	if totalSpace == 0 {
		log.Println("totalSpace unexpectedly zero")
		totalSpace = -1
	}
	availableSpace := int(availableBlocks)

	return 100-int(availableSpace*100/totalSpace)
}