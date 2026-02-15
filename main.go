package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const jsonVersion = "0.3"
const memThresholdPercent = 90
const diskThresholdPercent = 80
const cpuThresholdPercent = 90
const port = ":10321"
const endPoint = "/vitals"


func serveStats(resp http.ResponseWriter, req *http.Request) {
    io.WriteString(resp, statusAsJson())
}


func handleRequests() {
	// serve from root or endpoint
    http.HandleFunc(endPoint, serveStats)
	http.HandleFunc("/", serveStats)
    log.Fatal(http.ListenAndServe(port, nil))
}

func main() {
	handleRequests()
}


func statusAsJson() string {
	
	percentMemUsed := percentMemUsed()
	percentDiskUsed := percentDiskUsed()
	percentCpuUsed := percentCpuUsed()

	returnString := "{\"title\":\"vitals-glimpse\",\"version\":" + jsonVersion + ","

	if percentMemUsed < memThresholdPercent {
		returnString += "\"mem_status\":\"mem_okay\",\"mem_percent\":"
	} else {
		returnString += "\"mem_status\":\"mem_fail\",\"mem_percent\":"
	}
	returnString += fmt.Sprintf("%d,", percentMemUsed)
	
	if percentDiskUsed < diskThresholdPercent {
		returnString += "\"disk_status\":\"disk_okay\",\"disk_percent\":"
	} else {
		returnString += "\"disk_status\":\"disk_fail\",\"disk_percent\":"
	}
	returnString += fmt.Sprintf("%d,", percentDiskUsed)
	
	if percentCpuUsed < cpuThresholdPercent {
		returnString += "\"cpu_status\":\"cpu_okay\",\"cpu_percent\":"
	} else {
		returnString += "\"cpu_status\":\"cpu_fail\",\"cpu_percent\":"
	}
	returnString += fmt.Sprintf("%d}", percentCpuUsed)
	

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
	return 99-int(percentAvail)
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

	return 99-int(availableSpace*100/totalSpace)
}


func parseCPUFields(fields []string) (user, nice, system, idle, iowait, irq, softirq, steal int) {
	user, _ = strconv.Atoi(fields[1])
	nice, _ = strconv.Atoi(fields[2])
	system, _ = strconv.Atoi(fields[3])
	idle, _ = strconv.Atoi(fields[4])
	iowait, _ = strconv.Atoi(fields[5])
	irq, _ = strconv.Atoi(fields[6])
	softirq, _ = strconv.Atoi(fields[7])
	steal, _ = strconv.Atoi(fields[8])
	return
}

func getCPUTimes() (idleTime, totalTime int) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		log.Println("Error reading /proc/stat:", err)
		return
	}

	lines := strings.Split(string(contents), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "cpu" {
			user, nice, system, idle, iowait, irq, softirq, steal := parseCPUFields(fields)
			idleTime = idle + iowait
			totalTime = user + nice + system + idle + iowait + irq + softirq + steal
			return
		}
	}
	return
}

func isInContainer() bool {
	// Check if we have container-specific cgroup limits
	// If cpu.max exists and is set, we're likely in a limited container
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		line := strings.TrimSpace(string(data))
		// "max 100000" means no limit (bare metal or unlimited container)
		// "50000 100000" means limited (50% of 1 CPU)
		return line != "max 100000" && !strings.HasPrefix(line, "max ")
	}
	
	// Alternative: check if we're in a restricted namespace
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		// If we see paths like "/lxc/ct300" we're in a container
		return strings.Contains(string(data), "/lxc/") || 
		       strings.Contains(string(data), "/docker/")
	}
	
	return false
}

func getCGroupCPUUsage() (int64, error) {
	contents, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "usage_usec") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				return strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	return 0, nil
}

func percentCpuUsedCgroup() int {
	usageStart, _ := getCGroupCPUUsage()
	time.Sleep(1 * time.Second)
	usageEnd, _ := getCGroupCPUUsage()

	usageDelta := usageEnd - usageStart
	numCPUs := runtime.NumCPU()
	
	usage := int(100 * float64(usageDelta) / (1000000.0 * float64(numCPUs)))
	if usage > 100 {
		usage = 100
	}
	return usage
}

func percentCpuUsedProcStat() int {
	idleStart, totalStart := getCPUTimes()
	time.Sleep(1 * time.Second)
	idleEnd, totalEnd := getCPUTimes()

	idleDelta := idleEnd - idleStart
	totalDelta := totalEnd - totalStart

	if totalDelta == 0 {
		return 0
	}
	
	usage := int(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
	return usage
}

func percentCpuUsed() int {
	// Use cgroup stats in containers, /proc/stat on bare metal
	if isInContainer() {
		return percentCpuUsedCgroup()
	}
	return percentCpuUsedProcStat()
}