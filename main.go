package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const jsonVersion = "0.4"
const endPoint = "/vitals"

var memThresholdPercent = 90
var diskThresholdPercent = 80
var cpuThresholdPercent = 90
var port = 10321

var bindAddr = "0.0.0.0"
var apiKey string
var allowCIDRs string
var allowedNets []*net.IPNet
var rateLimit int

var runningInContainer bool

type rateLimiter struct {
	mu        sync.Mutex
	counts    map[string]int
	lastClean int64
}

var limiter = &rateLimiter{counts: make(map[string]int)}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().Unix() / 60
	key := ip + "|" + strconv.FormatInt(now, 10)

	if now != rl.lastClean {
		suffix := "|" + strconv.FormatInt(now, 10)
		for k := range rl.counts {
			if !strings.HasSuffix(k, suffix) {
				delete(rl.counts, k)
			}
		}
		rl.lastClean = now
	}

	rl.counts[key]++
	return rl.counts[key] <= rateLimit
}

func requireKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiKey != "" {
			provided := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func requireAllowedIP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(allowedNets) > 0 {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			allowed := false
			for _, n := range allowedNets {
				if n.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func isAllowedIP(r *http.Request) bool {
	if len(allowedNets) == 0 {
		return false
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	for _, n := range allowedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func rateCheck(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rateLimit > 0 && !isAllowedIP(r) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if !limiter.allow(host) {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
		}
		next(w, r)
	}
}


func serveStats(resp http.ResponseWriter, req *http.Request) {
    io.WriteString(resp, statusAsJson())
}


func handleRequests() {
	handler := requireAllowedIP(rateCheck(requireKey(serveStats)))
	http.HandleFunc(endPoint, handler)
	http.HandleFunc("/", handler)

	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	log.Printf("vitals-glimpse listening on %s", addr)
	if apiKey != "" {
		log.Println("  API key: required")
	}
	if len(allowedNets) > 0 {
		cidrs := make([]string, len(allowedNets))
		for i, n := range allowedNets {
			cidrs[i] = n.String()
		}
		log.Printf("  Allowed CIDRs: %s", strings.Join(cidrs, ", "))
	}
	if rateLimit > 0 {
		log.Printf("  Rate limit: %d req/min per IP", rateLimit)
	}

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func main() {
	flag.IntVar(&memThresholdPercent, "mem", 90, "memory usage threshold percent")
	flag.IntVar(&diskThresholdPercent, "disk", 80, "disk usage threshold percent")
	flag.IntVar(&cpuThresholdPercent, "cpu", 90, "cpu usage threshold percent")
	flag.IntVar(&port, "port", 10321, "server port")
	flag.StringVar(&bindAddr, "bind", "0.0.0.0", "address to bind to")
	flag.StringVar(&apiKey, "key", "", "API key required via X-API-Key header")
	flag.StringVar(&allowCIDRs, "allow", "", "comma-separated CIDR allowlist (e.g. \"10.0.0.0/24,192.168.1.0/24\")")
	flag.IntVar(&rateLimit, "ratelimit", 60, "max requests per IP per minute (0 to disable)")
	flag.Parse()

	if memThresholdPercent < 1 || memThresholdPercent > 100 {
		log.Fatalf("invalid -mem value %d: must be between 1 and 100", memThresholdPercent)
	}
	if diskThresholdPercent < 1 || diskThresholdPercent > 100 {
		log.Fatalf("invalid -disk value %d: must be between 1 and 100", diskThresholdPercent)
	}
	if cpuThresholdPercent < 1 || cpuThresholdPercent > 100 {
		log.Fatalf("invalid -cpu value %d: must be between 1 and 100", cpuThresholdPercent)
	}
	if port < 1 || port > 65535 {
		log.Fatalf("invalid -port value %d: must be between 1 and 65535", port)
	}
	if rateLimit < 0 {
		log.Fatalf("invalid -ratelimit value %d: must be >= 0", rateLimit)
	}

	if allowCIDRs != "" {
		for _, cidr := range strings.Split(allowCIDRs, ",") {
			cidr = strings.TrimSpace(cidr)
			if !strings.Contains(cidr, "/") {
				if strings.Contains(cidr, ":") {
					cidr += "/128"
				} else {
					cidr += "/32"
				}
			}
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				log.Fatalf("invalid -allow CIDR %q: %v", cidr, err)
			}
			allowedNets = append(allowedNets, network)
		}
	}

	runningInContainer = isInContainer()
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
	// systemd reliably sets this inside containers
	if data, err := os.ReadFile("/run/systemd/container"); err == nil {
		val := strings.TrimSpace(string(data))
		if val == "lxc" || val == "docker" || val == "oci" {
			return true
		}
	}

	// Docker-specific marker file
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Check cgroup v2 cpu limits
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		line := strings.TrimSpace(string(data))
		if line != "max 100000" && !strings.HasPrefix(line, "max ") {
			return true
		}
	}

	// Fallback: cgroup v1 paths
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "/lxc/") ||
			strings.Contains(content, "/docker/") {
			return true
		}
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

func hasCgroupCPUStats() bool {
	_, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	return err == nil
}

func percentCpuUsed() int {
	// Use cgroup stats in containers if available, /proc/stat otherwise
	if runningInContainer && hasCgroupCPUStats() {
		return percentCpuUsedCgroup()
	}
	return percentCpuUsedProcStat()
}